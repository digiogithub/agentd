import 'package:agentd_app/api/agentd_api.dart';
import 'package:agentd_app/state/session_view.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';

import 'session_fakes.dart';

final key = sessionKey('h1', 's');

Future<void> settle() => Future<void>.delayed(const Duration(milliseconds: 20));

ProviderContainer make(FakeSessionGateway gw) {
  final c = ProviderContainer(
    overrides: [
      // ignore: argument_type_not_assignable
      sessionGatewayProvider.overrideWith((ref, k) async => gw),
    ],
  );
  addTearDown(c.dispose);
  c.listen(sessionViewProvider(key), (_, _) {});
  return c;
}

void main() {
  test('full view then patch updates the state', () async {
    final gw = FakeSessionGateway();
    final c = make(gw);
    await settle();
    gw.sendView(makeView(lines: ['a', 'b']));
    await settle();
    expect(c.read(sessionViewProvider(key)).view!.lines.length, 2);
    gw.sendPatch({
      'from_revision': 1,
      'revision': 2,
      'epoch': 'e1',
      'first_line': 0,
      'splice': {
        'at': 2,
        'delete': 0,
        'insert': [
          {'text': 'c'},
        ],
      },
      'set': {'status': 'working'},
    });
    await settle();
    final s = c.read(sessionViewProvider(key));
    expect(s.view!.lines.map((l) => l.text), ['a', 'b', 'c']);
    expect(s.view!.status, Status.working);
    expect(s.view!.revision, 2);
  });

  test('patch from the wrong revision refetches', () async {
    final gw = FakeSessionGateway()..fetched = makeView(revision: 9);
    final c = make(gw);
    await settle();
    gw.sendView(makeView());
    await settle();
    gw.sendPatch({
      'from_revision': 5,
      'revision': 6,
      'epoch': 'e1',
      'first_line': 0,
    });
    await settle();
    expect(gw.fetches, 1);
    expect(c.read(sessionViewProvider(key)).view!.revision, 9);
  });

  test('resync refetches', () async {
    final gw = FakeSessionGateway()..fetched = makeView(revision: 4);
    final c = make(gw);
    await settle();
    gw.events.add(const SseEvent(event: 'resync', data: '{}'));
    await settle();
    expect(gw.fetches, 1);
    expect(c.read(sessionViewProvider(key)).view!.revision, 4);
  });

  test('reconnect reopens the stream and refetches', () async {
    final gw = FakeSessionGateway();
    final c = make(gw);
    await settle();
    expect(gw.opened, 1);
    await c.read(sessionViewProvider(key).notifier).reconnect();
    await settle();
    expect(gw.opened, 2);
    expect(gw.fetches, 1);
  });

  test('ack waits until the screen was visible for a while', () async {
    final gw = FakeSessionGateway();
    final c = make(gw);
    final n = c.read(sessionViewProvider(key).notifier);
    await settle();
    gw.sendView(makeView(attention: true));
    await settle();
    // Opened (e.g. by a deep link) but not yet "seen": no ack.
    n.setVisible(true);
    await Future<void>.delayed(const Duration(milliseconds: 700));
    expect(gw.acks, 0);
    await Future<void>.delayed(
      SessionViewNotifier.minVisibleBeforeAck + SessionViewNotifier.ackDebounce,
    );
    expect(gw.acks, 1);
    n.setVisible(false);
    gw.sendView(makeView(revision: 3, attention: true));
    await Future<void>.delayed(
      SessionViewNotifier.minVisibleBeforeAck + SessionViewNotifier.ackDebounce,
    );
    expect(gw.acks, 1);
  });

  test('leaving before the grace period never acks', () async {
    final gw = FakeSessionGateway();
    final c = make(gw);
    final n = c.read(sessionViewProvider(key).notifier);
    await settle();
    gw.sendView(makeView(attention: true));
    n.setVisible(true);
    await Future<void>.delayed(const Duration(milliseconds: 300));
    n.setVisible(false);
    await Future<void>.delayed(
      SessionViewNotifier.minVisibleBeforeAck + SessionViewNotifier.ackDebounce,
    );
    expect(gw.acks, 0);
  });

  test('choose sends id, label, revision and epoch', () async {
    final gw = FakeSessionGateway();
    final c = make(gw);
    await settle();
    gw.sendView(
      makeView(
        revision: 7,
        epoch: 'ep',
        status: Status.waiting,
        options: [optA, optB],
      ),
    );
    await settle();
    expect(await c.read(sessionViewProvider(key).notifier).choose(optB), true);
    final r = gw.acts.single.req;
    expect(r.action, ActAction.choose);
    expect(r.optionId, 'o2');
    expect(r.label, 'No');
    expect(r.revision, 7);
    expect(r.epoch, 'ep');
    expect(gw.acts.single.key, isNotEmpty);
  });

  test('conflict replaces the view and asks to review again', () async {
    final gw = FakeSessionGateway();
    final c = make(gw);
    await settle();
    gw.sendView(makeView(status: Status.waiting, options: [optA, optB]));
    await settle();
    final fresh = makeView(
      revision: 5,
      status: Status.waiting,
      options: [optB],
      lines: ['new menu'],
    );
    gw.actQueue.add(
      ApiException(
        statusCode: 409,
        code: 'stale_revision',
        message: 'x',
        details: {'view': fresh.toJson()},
      ),
    );
    final n = c.read(sessionViewProvider(key).notifier);
    expect(await n.choose(optA), false);
    final s = c.read(sessionViewProvider(key));
    expect(s.view!.revision, 5);
    expect(s.view!.options.length, 1);
    expect(s.reviewAgain, true);
    expect(s.feedback!.kind, FeedbackKind.review);
    expect(s.canRetry, false);
    expect(gw.acts.length, 1); // never resubmitted
    expect(gw.fetches, 0);
  });

  test('412 behaves like a conflict', () async {
    final gw = FakeSessionGateway();
    final c = make(gw);
    await settle();
    gw.sendView(makeView(status: Status.waiting, options: [optA]));
    await settle();
    gw.actQueue.add(
      ApiException(
        statusCode: 412,
        code: 'precondition_failed',
        message: 'x',
        details: {'view': makeView(revision: 8).toJson()},
      ),
    );
    await c.read(sessionViewProvider(key).notifier).choose(optA);
    expect(c.read(sessionViewProvider(key)).view!.revision, 8);
    expect(c.read(sessionViewProvider(key)).reviewAgain, true);
  });

  test('outcome_unknown is never retried; view refreshed', () async {
    final gw = FakeSessionGateway()..fetched = makeView(revision: 12);
    final c = make(gw);
    await settle();
    gw.sendView(makeView());
    await settle();
    gw.actQueue.add(ApiException.outcomeUnknown('lost'));
    final n = c.read(sessionViewProvider(key).notifier);
    // The text left the composer: it lives in the "result unknown" note.
    expect(await n.prompt('hi'), true);
    await settle();
    final s = c.read(sessionViewProvider(key));
    expect(s.canRetry, false);
    expect(s.unknownText, 'hi');
    expect(s.feedback!.message, contains('Result unknown'));
    expect(gw.acts.length, 1);
    expect(gw.fetches, 1);
    expect(await n.retry(), false);
    expect(gw.acts.length, 1);
    // Explicit "Send again": a NEW request (new key) on the displayed view.
    expect(await n.sendAgain(), true);
    expect(gw.acts.length, 2);
    expect(gw.acts[1].key, isNot(gw.acts[0].key));
    expect(gw.acts[1].req.text, 'hi');
    expect(gw.acts[1].req.revision, 12);
    expect(c.read(sessionViewProvider(key)).unknownText, isNull);
  });

  test('a second unknown result keeps the note, never auto sends', () async {
    final gw = FakeSessionGateway();
    final c = make(gw);
    await settle();
    gw.sendView(makeView());
    await settle();
    gw.actQueue
      ..add(ApiException.outcomeUnknown('lost'))
      ..add(ApiException.outcomeUnknown('lost again'));
    final n = c.read(sessionViewProvider(key).notifier);
    await n.prompt('hi');
    await n.sendAgain();
    await settle();
    expect(c.read(sessionViewProvider(key)).unknownText, 'hi');
    await settle();
    expect(gw.acts.length, 2);
  });

  test('busy never retries by itself; Send again uses a new key', () async {
    final gw = FakeSessionGateway();
    final c = make(gw);
    await settle();
    gw.sendView(makeView());
    await settle();
    gw.actQueue.add(
      ApiException(statusCode: 409, code: 'busy', message: 'draft'),
    );
    final n = c.read(sessionViewProvider(key).notifier);
    // False: the text stays in the composer.
    expect(await n.prompt('hi'), false);
    final s = c.read(sessionViewProvider(key));
    expect(s.canRetry, true);
    expect(s.feedback!.message, contains('try again when they finish'));
    // The draft clearing on the host does NOT send anything.
    gw.sendView(makeView(revision: 2));
    await settle();
    await settle();
    expect(gw.acts.length, 1);
    // The user edits and taps Send: a new key and the displayed revision.
    expect(await n.prompt('hi there'), true);
    expect(gw.acts.length, 2);
    expect(gw.acts[1].key, isNot(gw.acts[0].key));
    expect(gw.acts[1].req.revision, 2);
  });

  test('explicit Retry of a busy prompt reuses the key within 30 s', () async {
    var now = DateTime(2026, 1, 1, 12);
    final gw = FakeSessionGateway();
    final c = ProviderContainer(
      overrides: [
        // ignore: argument_type_not_assignable
        sessionGatewayProvider.overrideWith((ref, k) async => gw),
        sessionClockProvider.overrideWithValue(() => now),
      ],
    );
    addTearDown(c.dispose);
    c.listen(sessionViewProvider(key), (_, _) {});
    await settle();
    gw.sendView(makeView());
    await settle();
    gw.actQueue.add(
      ApiException(statusCode: 409, code: 'busy', message: 'draft'),
    );
    final n = c.read(sessionViewProvider(key).notifier);
    await n.prompt('hi');
    now = now.add(const Duration(seconds: 10));
    expect(await n.retry(), true);
    expect(gw.acts[1].key, gw.acts[0].key);
    expect(gw.acts[1].req.text, 'hi');
    expect(gw.acts[1].req.revision, gw.acts[0].req.revision);

    // Too old: no silent reuse; the user must tap Send again.
    gw.actQueue.add(
      ApiException(statusCode: 409, code: 'busy', message: 'draft'),
    );
    await n.prompt('later');
    now = now.add(const Duration(seconds: 31));
    expect(await n.retry(), false);
    expect(gw.acts.length, 3);
    expect(c.read(sessionViewProvider(key)).canRetry, false);
  });

  test(
    'every act carries the revision and epoch of the displayed view',
    () async {
      final gw = FakeSessionGateway()
        ..fetched = makeView(revision: 4, epoch: 'ep', status: Status.working);
      final c = make(gw);
      await settle();
      gw.sendView(makeView(revision: 4, epoch: 'ep', status: Status.working));
      await settle();
      final n = c.read(sessionViewProvider(key).notifier);
      await n.prompt('p');
      await n.keys(['esc']);
      await n.interrupt();
      await n.cancelMenu();
      expect(gw.acts.length, 4);
      for (final a in gw.acts) {
        expect(a.req.revision, 4, reason: a.req.action.wire);
        expect(a.req.epoch, 'ep', reason: a.req.action.wire);
        expect(a.req.toJson()['revision'], 4);
      }
    },
  );

  test('a view that moved on during a confirm dialog sends nothing', () async {
    final gw = FakeSessionGateway();
    final c = make(gw);
    await settle();
    final shown = makeView(
      revision: 3,
      status: Status.waiting,
      options: [optA, optB],
    );
    gw.sendView(shown);
    await settle();
    // The user tapped on `shown`, a dialog opened, and meanwhile...
    gw.sendView(makeView(revision: 4, status: Status.waiting, options: [optA]));
    await settle();
    final n = c.read(sessionViewProvider(key).notifier);
    expect(await n.choose(optB, shown: shown), false);
    expect(await n.interrupt(shown: shown), false);
    expect(gw.acts, isEmpty);
    final s = c.read(sessionViewProvider(key));
    expect(s.reviewAgain, true);
    expect(s.feedback!.kind, FeedbackKind.review);
    // On the unchanged view it goes through with that view's revision.
    expect(await n.choose(optA, shown: s.view), true);
    expect(gw.acts.single.req.revision, 4);
  });

  test('keys, interrupt and cancel are never offered a Retry', () async {
    final gw = FakeSessionGateway();
    final c = make(gw);
    await settle();
    gw.sendView(makeView(status: Status.working));
    await settle();
    final n = c.read(sessionViewProvider(key).notifier);
    for (final err in [
      ApiException(statusCode: 409, code: 'busy', message: 'x'),
      ApiException(statusCode: 409, code: 'starting', message: 'x'),
      ApiException(statusCode: 409, code: 'in_progress', message: 'x'),
      ApiException(statusCode: 503, code: 'paste_unverified', message: 'x'),
      ConnectionException('down'),
    ]) {
      gw.actQueue.add(err);
      await n.keys(['esc']);
      expect(c.read(sessionViewProvider(key)).canRetry, false);
      expect(await n.retry(), false);
      gw.actQueue.add(err);
      await n.interrupt();
      expect(c.read(sessionViewProvider(key)).canRetry, false);
      gw.actQueue.add(err);
      await n.cancelMenu();
      expect(c.read(sessionViewProvider(key)).canRetry, false);
    }
    expect(gw.acts.length, 15);
  });

  test('in_progress says still processing; unknown 409 is generic', () async {
    final gw = FakeSessionGateway();
    final c = make(gw);
    await settle();
    gw.sendView(makeView());
    await settle();
    final n = c.read(sessionViewProvider(key).notifier);
    gw.actQueue.add(
      ApiException(statusCode: 409, code: 'in_progress', message: 'x'),
    );
    await n.prompt('hi');
    var s = c.read(sessionViewProvider(key));
    expect(s.feedback!.message, contains('Still processing'));
    expect(s.reviewAgain, false);
    // A prompt may be retried later with the same key.
    expect(s.canRetry, true);
    await n.retry();
    expect(gw.acts[1].key, gw.acts[0].key);

    gw.actQueue.add(
      ApiException(statusCode: 409, code: 'weird_new_code', message: 'x'),
    );
    await n.prompt('again');
    s = c.read(sessionViewProvider(key));
    expect(s.feedback!.message, contains('weird_new_code'));
    expect(s.feedback!.kind, FeedbackKind.error);
    expect(s.reviewAgain, false);
    expect(s.canRetry, false);
  });

  test('manual retry reuses the idempotency key', () async {
    final gw = FakeSessionGateway();
    final c = make(gw);
    await settle();
    gw.sendView(makeView());
    await settle();
    gw.actQueue.add(
      ApiException(statusCode: 409, code: 'not_ready', message: 'x'),
    );
    gw.actQueue.add(
      ApiException(statusCode: 503, code: 'paste_unverified', message: 'x'),
    );
    final n = c.read(sessionViewProvider(key).notifier);
    await n.prompt('hello');
    expect(c.read(sessionViewProvider(key)).canRetry, false);
    await n.prompt('again');
    expect(c.read(sessionViewProvider(key)).canRetry, true);
    await n.retry();
    expect(gw.acts[2].key, gw.acts[1].key);
    expect(gw.acts[2].req.text, 'again');
    await n.prompt('new');
    expect(gw.acts[3].key, isNot(gw.acts[1].key));
  });

  test('terminal stream error is surfaced', () async {
    final gw = FakeSessionGateway();
    final c = make(gw);
    await settle();
    gw.events.addError(
      ApiException(statusCode: 404, code: 'not_found', message: 'x'),
    );
    await settle();
    expect(
      c.read(sessionViewProvider(key)).error,
      contains('no longer exists'),
    );
  });
}
