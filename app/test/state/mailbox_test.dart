import 'package:agentd_app/api/agentd_api.dart';
import 'package:agentd_app/state/mailbox.dart';
import 'package:agentd_app/state/providers.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';

import '../support/fakes.dart';
import 'launch_support.dart';

void main() {
  late FakeClient a;
  late FakeClient b;
  late ProviderContainer ct;
  late ProviderSubscription<MailboxActions> keep;
  setUp(() {
    a = FakeClient();
    b = FakeClient();
    ct = ProviderContainer(
      overrides: [
        clients({'h1': a, 'h2': b}),
        hostsProvider.overrideWith(
          () => FakeHosts([sampleHost(), sampleHost(id: 'h2', label: 'two')]),
        ),
      ],
    );
    addTearDown(ct.dispose);
    ct.listen(mailboxFeedProvider, (_, _) {});
    // A screen watching the actions keeps the idempotency keys alive.
    keep = ct.listen(mailboxActionsProvider, (_, _) {});
  });
  MailboxFeed feed() => ct.read(mailboxFeedProvider.notifier);
  MailboxFeedState st() => ct.read(mailboxFeedProvider);

  test('merges hosts newest first, counts unread, pages per host', () async {
    a.pages = {
      null: MessagePage(
        messages: [
          msg(5),
          msg(3, state: MessageState.read),
        ],
        nextBefore: 3,
      ),
      3: MessagePage(messages: [msg(1)]),
    };
    b.pages = {
      null: MessagePage(messages: [msg(4), msg(2)]),
    };
    await feed().refresh();
    expect(
      [for (final i in st().items) (i.hostId, i.message.id)],
      [('h1', 5), ('h2', 4), ('h1', 3), ('h2', 2)],
    );
    expect(st().unreadCount, 3);
    expect(st().hasMore, isTrue);
    await feed().loadMore();
    expect(st().items.last.message.id, 1);
    expect(st().hasMore, isFalse);
    expect(a.listCalls.last.before, 3);
    expect(b.listCalls.length, 1);
  });

  test('one host failing keeps the other', () async {
    a.pages = {
      null: MessagePage(messages: [msg(1)]),
    };
    b.pages = {};
    final c2 = ProviderContainer(
      retry: (_, _) => null,
      overrides: [
        hostClientProvider.overrideWith((ref, id) async {
          if (id == 'h2') throw apiErr(401, 'unauthenticated');
          return a;
        }),
        hostsProvider.overrideWith(
          () => FakeHosts([sampleHost(), sampleHost(id: 'h2')]),
        ),
      ],
    );
    addTearDown(c2.dispose);
    c2.listen(mailboxFeedProvider, (_, _) {});
    await c2.read(mailboxFeedProvider.notifier).refresh();
    final s = c2.read(mailboxFeedProvider);
    expect(s.items.length, 1);
    expect(s.errors.keys, ['h2']);
  });

  test('markRead updates the cached message', () async {
    a.pages = {
      null: MessagePage(messages: [msg(5)]),
    };
    await feed().refresh();
    await ct.read(mailboxActionsProvider).markRead('h1', 5);
    expect(a.readCalls, [5]);
    expect(st().unreadCount, 0);
  });

  test(
    'reply key is stable on retry, new for other text, fresh after ok',
    () async {
      final act = ct.read(mailboxActionsProvider);
      a.replyError = ApiException.outcomeUnknown('x');
      final r1 = await act.reply('h1', 7, 'hello');
      expect((r1 as MailFailed).outcomeUnknown, isTrue);
      await act.reply('h1', 7, 'hello');
      await act.reply('h1', 7, 'other');
      a.replyError = null;
      await act.reply('h1', 7, 'other');
      await act.reply('h1', 7, 'other');
      final k = [for (final c in a.replyCalls) c.key];
      expect(k[0], k[1]);
      expect(k[2], isNot(k[0]));
      expect(k[3], k[2]);
      expect(k[4], isNot(k[3]));
    },
  );

  test('send key stable on retry', () async {
    final act = ct.read(mailboxActionsProvider);
    const req = SendRequest(to: 'codex@webapp', body: 'hi');
    a.sendError = apiErr(503, 'daemon_unavailable');
    await act.send('h1', req);
    await act.send('h1', req);
    a.sendError = null;
    await act.send('h1', req);
    expect(a.sendCalls[0].key, a.sendCalls[1].key);
    expect(a.sendCalls[2].key, a.sendCalls[0].key);
    await act.send('h1', req);
    expect(a.sendCalls[3].key, isNot(a.sendCalls[0].key));
  });

  test('recipient validation', () {
    expect(validRecipient('codex@webapp'), isTrue);
    expect(validRecipient('human:jose'), isTrue);
    expect(validRecipient('human:'), isFalse);
    expect(validRecipient('a b'), isFalse);
  });

  test(
    'send keys die with the compose screen; fresh sends a new key',
    () async {
      const req = SendRequest(to: 'codex@webapp', body: 'hi');
      final first = ct.read(mailboxActionsProvider);
      a.sendError = ApiException.outcomeUnknown('x');
      await first.send('h1', req);
      await first.send('h1', req);
      expect(a.sendCalls[1].key, a.sendCalls[0].key);
      // Explicit "Send again (new message)".
      await first.send('h1', req, fresh: true);
      expect(a.sendCalls[2].key, isNot(a.sendCalls[0].key));
      // The screen goes away: the provider (and its keys) are disposed.
      keep.close();
      await Future<void>.delayed(Duration.zero);
      final second = ct.read(mailboxActionsProvider);
      expect(identical(first, second), isFalse);
      await second.send('h1', req);
      expect(a.sendCalls[3].key, isNot(a.sendCalls[2].key));
    },
  );
}
