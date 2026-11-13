import 'package:agentd_app/api/agentd_api.dart';
import 'package:agentd_app/state/mailbox.dart';
import 'package:agentd_app/state/providers.dart';
import 'package:agentd_app/ui/mailbox/compose_screen.dart';
import 'package:agentd_app/ui/mailbox/mailbox_screen.dart';
import 'package:agentd_app/ui/mailbox/message_screen.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:material_ui/material_ui.dart';

import '../state/launch_support.dart';
import '../support/fakes.dart';

Future<void> pump(
  WidgetTester t,
  Map<String, FakeClient> cl,
  String initial, {
  int pageSize = 50,
  String scope = 'write',
}) async {
  final router = GoRouter(
    initialLocation: initial,
    routes: [
      GoRoute(path: '/mailbox', builder: (_, _) => const MailboxScreen()),
      GoRoute(
        path: '/mailbox/compose',
        builder: (_, s) => ComposeScreen(hostId: s.uri.queryParameters['host']),
      ),
      GoRoute(
        path: '/hosts/:id/mailbox/:msgId',
        builder: (_, s) => MessageScreen(
          hostId: s.pathParameters['id']!,
          msgId: int.parse(s.pathParameters['msgId']!),
        ),
      ),
    ],
  );
  addTearDown(router.dispose);
  await t.pumpWidget(
    ProviderScope(
      retry: (_, _) => null,
      overrides: [
        clients(cl),
        mailboxPageSizeProvider.overrideWithValue(pageSize),
        hostsProvider.overrideWith(
          () => FakeHosts([
            for (final id in cl.keys) sampleHost(id: id, label: 'host-$id'),
          ]),
        ),
      ],
      child: MaterialApp.router(routerConfig: router),
    ),
  );
  await t.pumpAndSettle();
}

void main() {
  testWidgets('list merges hosts, shows unread badge and loads more', (
    t,
  ) async {
    final a = FakeClient()
      ..pages = {
        null: MessagePage(messages: [msg(5, subject: 'Hello')], nextBefore: 5),
        5: MessagePage(messages: [msg(1, subject: 'Old')]),
      };
    final b = FakeClient()
      ..pages = {
        null: MessagePage(messages: [msg(4, state: MessageState.read)]),
      };
    await pump(t, {'h1': a, 'h2': b}, '/mailbox');
    expect(find.text('Hello'), findsOneWidget);
    expect(find.text('body 4'), findsOneWidget);
    expect(find.byKey(const ValueKey('unread-badge')), findsOneWidget);
    expect(find.text('1'), findsWidgets);
    expect(find.text('Old'), findsNothing);
    await t.ensureVisible(find.byKey(const ValueKey('load-more')));
    await t.tap(find.byKey(const ValueKey('load-more')));
    await t.pumpAndSettle();
    expect(find.text('Old'), findsOneWidget);
    expect(find.byKey(const ValueKey('load-more')), findsNothing);
  });

  testWidgets('empty state', (t) async {
    await pump(t, {'h1': FakeClient()}, '/mailbox');
    expect(find.text('No messages'), findsOneWidget);
  });

  testWidgets('detail: selectable body, explicit mark read, reply', (t) async {
    final c = FakeClient()..messages = {7: msg(7, subject: 'Question')};
    await pump(t, {'h1': c}, '/hosts/h1/mailbox/7');
    expect(find.byType(SelectableText), findsOneWidget);
    expect(find.text('body 7'), findsOneWidget);
    expect(c.readCalls, isEmpty);

    await t.tap(find.byKey(const ValueKey('mark-read')));
    await t.pumpAndSettle();
    expect(c.readCalls, [7]);

    await t.enterText(find.byKey(const ValueKey('reply')), 'answer');
    await t.tap(find.byKey(const ValueKey('send-reply')));
    await t.pumpAndSettle();
    expect(c.replyCalls.single.body, 'answer');
    expect(c.replyCalls.single.key, isNotEmpty);
  });

  testWidgets('reply failure keeps text; retry reuses the key', (t) async {
    final c = FakeClient()
      ..messages = {7: msg(7)}
      ..replyError = ApiException.outcomeUnknown('x');
    await pump(t, {'h1': c}, '/hosts/h1/mailbox/7');
    await t.enterText(find.byKey(const ValueKey('reply')), 'answer');
    await t.tap(find.byKey(const ValueKey('send-reply')));
    await t.pumpAndSettle();
    expect(find.byKey(const ValueKey('mail-error')), findsOneWidget);
    await t.tap(find.byKey(const ValueKey('send-reply')));
    await t.pumpAndSettle();
    expect(c.replyCalls.length, 2);
    expect(c.replyCalls[0].key, c.replyCalls[1].key);
  });

  testWidgets('compose validates and sends with picker', (t) async {
    final c = FakeClient()
      ..sessions = [const SessionSummary(name: 'codex@webapp')];
    await pump(t, {'h1': c}, '/mailbox/compose?host=h1');
    await t.tap(find.byKey(const ValueKey('send')));
    await t.pumpAndSettle();
    expect(find.text('Pick or type a recipient'), findsOneWidget);
    expect(find.text('Write a message'), findsOneWidget);

    await t.tap(find.byKey(const ValueKey('to-codex@webapp')));
    await t.enterText(find.byKey(const ValueKey('compose-body')), 'ping');
    await t.enterText(find.byKey(const ValueKey('subject')), 'S');
    await t.tap(find.byKey(const ValueKey('send')));
    await t.pumpAndSettle();
    final call = c.sendCalls.single;
    expect(call.req.to, 'codex@webapp');
    expect(call.req.subject, 'S');
    expect(call.req.body, 'ping');
    expect(call.key, isNotEmpty);
  });

  testWidgets('compose: a lost answer offers Send again (new message)', (
    t,
  ) async {
    final c = FakeClient()
      ..sendError = ApiException.outcomeUnknown('lost')
      ..sessions = [const SessionSummary(name: 'codex@webapp')];
    await pump(t, {'h1': c}, '/mailbox/compose?host=h1');
    await t.tap(find.byKey(const ValueKey('to-codex@webapp')));
    await t.enterText(find.byKey(const ValueKey('compose-body')), 'ping');
    expect(find.byKey(const ValueKey('send-again-new')), findsNothing);
    await t.tap(find.byKey(const ValueKey('send')));
    await t.pumpAndSettle();
    expect(find.byKey(const ValueKey('send-again-new')), findsOneWidget);
    // Plain Send retries the same request (deduplicated by the host).
    await t.tap(find.byKey(const ValueKey('send')));
    await t.pumpAndSettle();
    expect(c.sendCalls[1].key, c.sendCalls[0].key);
    // The explicit button sends a second, distinct message.
    await t.ensureVisible(find.byKey(const ValueKey('send-again-new')));
    await t.tap(find.byKey(const ValueKey('send-again-new')));
    await t.pumpAndSettle();
    expect(c.sendCalls.length, 3);
    expect(c.sendCalls[2].key, isNot(c.sendCalls[0].key));
  });

  testWidgets('compose offers human identity and rejects bad recipient', (
    t,
  ) async {
    await pump(t, {'h1': FakeClient()}, '/mailbox/compose?host=h1');
    expect(find.byKey(const ValueKey('to-me')), findsOneWidget);
    await t.enterText(find.byKey(const ValueKey('to')), 'bad name');
    await t.enterText(find.byKey(const ValueKey('compose-body')), 'x');
    await t.tap(find.byKey(const ValueKey('send')));
    await t.pumpAndSettle();
    expect(find.text('Use a session name or human:<name>'), findsOneWidget);
  });

  testWidgets('peek action only for admin hosts', (t) async {
    await pump(t, {'h1': FakeClient()}, '/mailbox');
    expect(find.byTooltip('Peek at another mailbox'), findsNothing);
  });
}
