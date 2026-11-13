import 'package:agentd_app/api/agentd_api.dart';
import 'package:agentd_app/state/agents_model.dart';
import 'package:agentd_app/state/notifications_model.dart';
import 'package:flutter_test/flutter_test.dart';

import '../support/agents_fakes.dart';

AgentEntry entry(
  String name, {
  String host = 'h1',
  Status status = Status.idle,
  bool attention = false,
  String? reason,
  String? preview,
}) => AgentEntry(
  hostId: host,
  hostLabel: 'box',
  session: sess(name, status: status, attention: attention, preview: preview),
  hostOnline: true,
  attention: attention,
  attentionReason: reason,
);

AgentsState state(List<AgentEntry> e, {bool loaded = true}) => AgentsState(
  entries: e,
  hosts: {'h1': HostFeedInfo(online: true, loaded: loaded)},
  loading: false,
);

AttentionEvent ev(
  String s, {
  NotifyReason r = NotifyReason.waiting,
  String host = 'h1',
  String? preview,
}) => AttentionEvent(
  hostId: host,
  hostLabel: 'box',
  session: s,
  reason: r,
  preview: preview,
);

void main() {
  group('AttentionTracker', () {
    test('first loaded snapshot is a baseline', () {
      final t = AttentionTracker();
      expect(
        t.update(state([entry('a', attention: true)], loaded: false)),
        isEmpty,
      );
      expect(t.update(state([entry('a', attention: true)])), isEmpty);
      expect(t.update(state([entry('a', attention: true)])), isEmpty);
    });

    test('attention rising emits once, with the wire reason', () {
      final t = AttentionTracker()..update(state([entry('a')]));
      final e = t.update(state([entry('a', attention: true, reason: 'bell')]));
      expect(e.single.reason, NotifyReason.bell);
      expect(e.single.session, 'a');
      expect(
        t.update(state([entry('a', attention: true, reason: 'bell')])),
        isEmpty,
      );
    });

    test('reason falls back on the status', () {
      final t = AttentionTracker()..update(state([entry('a'), entry('b')]));
      final e = t.update(
        state([
          entry('a', attention: true, status: Status.waiting),
          entry('b', attention: true),
        ]),
      );
      expect(e.map((x) => x.reason), [
        NotifyReason.waiting,
        NotifyReason.turnDone,
      ]);
    });

    test('exit emits once; cleared then raised again emits again', () {
      final t = AttentionTracker()
        ..update(state([entry('a', status: Status.working)]));
      expect(
        t.update(state([entry('a', status: Status.exited)])).single.reason,
        NotifyReason.exited,
      );
      expect(t.update(state([entry('a', status: Status.exited)])), isEmpty);
      final t2 = AttentionTracker()..update(state([entry('a')]));
      expect(t2.update(state([entry('a', attention: true)])), hasLength(1));
      expect(t2.update(state([entry('a')])), isEmpty);
      expect(t2.update(state([entry('a', attention: true)])), hasLength(1));
    });
  });

  group('NotificationPolicy', () {
    late DateTime now;
    late NotificationPolicy p;
    setUp(() {
      now = DateTime.utc(2026, 11, 13);
      p = NotificationPolicy(now: () => now);
    });

    test('title, body and payload carry no screen content by default', () {
      final r = p.decide(
        ev('codex@web', preview: 'SECRET screen text'),
        const NotifySettings(),
      )!;
      expect(r.title, 'codex@web on box');
      expect(r.body, 'codex@web is waiting for you');
      expect(r.body, isNot(contains('SECRET')));
      expect(r.payload, 'agentd://open?h=h1&s=codex%40web');
    });

    test('preview only when enabled', () {
      final r = p.decide(
        ev('a', preview: ' Proceed? '),
        const NotifySettings(includePreview: true),
      )!;
      expect(r.body, endsWith(': Proceed?'));
    });

    test('debounce per session and reason for 30 s', () {
      const s = NotifySettings();
      expect(p.decide(ev('a'), s), isNotNull);
      now = now.add(const Duration(seconds: 29));
      expect(p.decide(ev('a'), s), isNull);
      expect(p.decide(ev('a', r: NotifyReason.exited), s), isNotNull);
      expect(p.decide(ev('b'), s), isNotNull);
      now = now.add(const Duration(seconds: 2));
      expect(p.decide(ev('a'), s), isNotNull);
    });

    test('global and per-host mute', () {
      expect(p.decide(ev('a'), const NotifySettings(muted: true)), isNull);
      expect(
        p.decide(ev('a'), const NotifySettings(mutedHosts: {'h1'})),
        isNull,
      );
      expect(
        p.decide(ev('a', host: 'h2'), const NotifySettings(mutedHosts: {'h1'})),
        isNotNull,
      );
    });

    test('a muted event does not start the debounce', () {
      expect(p.decide(ev('a'), const NotifySettings(muted: true)), isNull);
      expect(p.decide(ev('a'), const NotifySettings()), isNotNull);
    });

    test('the visible session is suppressed, others are not', () {
      const s = NotifySettings();
      expect(p.decide(ev('a'), s, visibleKey: 'h1/a'), isNull);
      expect(p.decide(ev('b'), s, visibleKey: 'h1/a'), isNotNull);
    });
  });

  test('settings json round trip and defaults', () {
    const d = NotifySettings();
    expect(d.muted, isFalse);
    expect(d.includePreview, isFalse);
    expect(d.keepInTray, isTrue);
    expect(d.foregroundService, isFalse);
    final s = NotifySettings.fromJson(
      const NotifySettings(
        muted: true,
        mutedHosts: {'b', 'a'},
        includePreview: true,
        keepInTray: false,
      ).toJson(),
    );
    expect(s.muted && s.includePreview && !s.keepInTray, isTrue);
    expect(s.mutedHosts, {'a', 'b'});
    expect(NotifySettings.fromJson(const {'muted_hosts': 5}), d);
  });

  test('sessionKeyOfLocation', () {
    expect(
      sessionKeyOfLocation('/hosts/h1/sessions/codex%40web'),
      'h1/codex@web',
    );
    expect(sessionKeyOfLocation('/hosts'), isNull);
    expect(sessionKeyOfLocation('/hosts/h1/launch'), isNull);
  });

  group('TrayModel', () {
    test('no attention: open, mute, quit', () {
      final m = TrayModel.from(state([entry('a')]), const NotifySettings());
      expect(m.count, 0);
      expect(m.tooltip, 'agentd');
      expect(m.items.map((i) => i.label), [
        'Open agentd',
        'Mute notifications',
        'Quit',
      ]);
    });

    test(
      'attention sessions are listed and counted; muted flips the label',
      () {
        final m = TrayModel.from(
          state([
            entry('a', attention: true),
            entry('b'),
            entry('c', attention: true),
          ]),
          const NotifySettings(muted: true),
        );
        expect(m.count, 2);
        expect(m.tooltip, 'agentd: 2 need attention');
        final sessions = m.items.where((i) => i.action == TrayAction.session);
        expect(sessions.map((i) => i.label), ['a on box', 'c on box']);
        expect(sessions.first.hostId, 'h1');
        expect(m.items[m.items.length - 2].label, 'Unmute notifications');
        expect(m.items.last.action, TrayAction.quit);
      },
    );
  });
}
