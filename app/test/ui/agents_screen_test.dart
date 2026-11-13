import 'package:agentd_app/api/agentd_api.dart';
import 'package:agentd_app/state/mailbox.dart';
import 'package:agentd_app/ui/agents/agents_screen.dart';
import 'package:agentd_app/ui/agents/agent_row.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:material_ui/material_ui.dart';

import '../support/agents_fakes.dart';
import '../support/fakes.dart';

final _now = DateTime.utc(2026, 11, 13, 12);

void main() {
  late FakeFeeds feeds;
  late GoRouter router;

  Future<void> pump(
    WidgetTester t,
    List<dynamic> hosts, {
    List<dynamic> extra = const [],
  }) async {
    router = GoRouter(
      routes: [
        GoRoute(path: '/', builder: (c, s) => const AgentsScreen()),
        GoRoute(path: '/pair', builder: (c, s) => const Text('PAIR PAGE')),
        GoRoute(path: '/hosts', builder: (c, s) => const Text('HOSTS PAGE')),
        GoRoute(
          path: '/mailbox',
          builder: (c, s) => const Text('MAILBOX PAGE'),
        ),
        GoRoute(
          path: '/hosts/:id/launch',
          builder: (c, s) => Text('LAUNCH ${s.pathParameters['id']}'),
        ),
        GoRoute(
          path: '/hosts/:id/sessions/:name',
          builder: (c, s) => Text(
            'SESSION ${s.pathParameters['id']} ${s.pathParameters['name']}',
          ),
        ),
      ],
    );
    await t.pumpWidget(
      ProviderScope(
        // ignore: argument_type_not_assignable
        overrides: [
          ...agentsOverrides(feeds, hosts.cast()),
          agentsClockProvider.overrideWithValue(() => _now),
          ...extra,
        ].cast(),
        child: MaterialApp.router(routerConfig: router),
      ),
    );
    for (var i = 0; i < 5; i++) {
      await t.pump(const Duration(milliseconds: 10));
    }
  }

  setUp(() => feeds = FakeFeeds());

  group('app bar and launch entry points', () {
    testWidgets('mailbox button shows the unread badge and navigates', (
      t,
    ) async {
      await pump(
        t,
        [sampleHost()],
        extra: [mailboxUnreadProvider.overrideWithValue(3)],
      );
      final badge = t.widget<Badge>(find.byKey(const Key('mailbox-badge')));
      expect(badge.isLabelVisible, isTrue);
      expect((badge.label! as Text).data, '3');
      await t.tap(find.byTooltip('Mailbox'));
      await t.pumpAndSettle();
      expect(find.text('MAILBOX PAGE'), findsOneWidget);
    });

    testWidgets('no unread: the badge is hidden', (t) async {
      await pump(
        t,
        [sampleHost()],
        extra: [mailboxUnreadProvider.overrideWithValue(0)],
      );
      final badge = t.widget<Badge>(find.byKey(const Key('mailbox-badge')));
      expect(badge.isLabelVisible, isFalse);
    });

    testWidgets('hosts button navigates to /hosts', (t) async {
      await pump(t, [sampleHost()]);
      await t.tap(find.byTooltip('Hosts'));
      await t.pumpAndSettle();
      expect(find.text('HOSTS PAGE'), findsOneWidget);
    });

    testWidgets('launch button with one host goes straight to its launch', (
      t,
    ) async {
      feeds.lists['h1'] = [sess('a')];
      await pump(t, [sampleHost()]);
      await t.tap(find.byKey(const Key('launch-fab')));
      await t.pumpAndSettle();
      expect(find.text('LAUNCH h1'), findsOneWidget);
    });

    testWidgets('launch button with several hosts asks which one', (t) async {
      await pump(t, [sampleHost(), sampleHost(id: 'h2', label: 'laptop')]);
      await t.tap(find.byKey(const Key('launch-fab')));
      await t.pumpAndSettle();
      expect(find.text('Launch on which host?'), findsOneWidget);
      await t.tap(find.text('laptop'));
      await t.pumpAndSettle();
      expect(find.text('LAUNCH h2'), findsOneWidget);
    });

    testWidgets('no hosts: no launch button', (t) async {
      await pump(t, []);
      expect(find.byKey(const Key('launch-fab')), findsNothing);
    });
  });

  testWidgets('no hosts: pair CTA navigates to /pair', (t) async {
    await pump(t, []);
    expect(find.text('No hosts paired'), findsOneWidget);
    await t.tap(find.text('Pair a host'));
    await t.pumpAndSettle();
    expect(find.text('PAIR PAGE'), findsOneWidget);
  });

  testWidgets('no sessions: launch CTA navigates to the launch route', (
    t,
  ) async {
    feeds.lists['h1'] = [];
    await pump(t, [sampleHost()]);
    expect(find.text('No agents running'), findsOneWidget);
    await t.tap(find.text('Launch an agent'));
    await t.pumpAndSettle();
    expect(find.text('LAUNCH h1'), findsOneWidget);
  });

  testWidgets('no sessions with several hosts asks which host', (t) async {
    await pump(t, [sampleHost(), sampleHost(id: 'h2', label: 'laptop')]);
    await t.tap(find.text('Launch an agent'));
    await t.pumpAndSettle();
    await t.tap(find.text('laptop'));
    await t.pumpAndSettle();
    expect(find.text('LAUNCH h2'), findsOneWidget);
  });

  testWidgets('badges show text for every status', (t) async {
    feeds.lists['h1'] = [
      sess('a@p', status: Status.working),
      sess('b@p', status: Status.idle),
      sess('c@p', status: Status.waiting),
      sess('d@p', status: Status.starting),
      sess('e@p', status: Status.exited, state: SessionState.exited),
      sess(
        'f@p',
        status: Status.exited,
        state: SessionState.exited,
        exitCode: 3,
      ),
    ];
    await pump(t, [sampleHost()]);
    for (final s in [
      'Working',
      'Idle',
      'Waiting',
      'Starting',
      'Exited',
      'Exited 3',
    ]) {
      expect(find.text(s), findsOneWidget, reason: s);
    }
    expect(
      find.bySemanticsLabel(RegExp(r'^c@p, on box, Waiting')),
      findsOneWidget,
    );
  });

  testWidgets('attention rows sort first and show a dot', (t) async {
    feeds.lists['h1'] = [
      sess('idle@p', status: Status.idle),
      sess('wait@p', status: Status.waiting),
      sess('att@p', status: Status.idle, attention: true),
    ];
    await pump(t, [sampleHost()]);
    final ys = [
      for (final n in ['att@p', 'wait@p', 'idle@p'])
        t.getTopLeft(find.text(n)).dy,
    ];
    expect(ys, [...ys]..sort());
    expect(find.byKey(const Key('attention-dot')), findsOneWidget);
  });

  testWidgets('row shows host, project, preview and relative time', (t) async {
    feeds.lists['h1'] = [
      sess(
        'a@proj',
        preview: 'line one\n   line two',
        lastOutput: _now.subtract(const Duration(minutes: 5)),
      ),
    ];
    await pump(t, [sampleHost()]);
    expect(find.text('box · proj · 5m ago'), findsOneWidget);
    expect(find.text('line one line two'), findsOneWidget);
    final txt = t.widget<Text>(find.text('line one line two'));
    expect(txt.maxLines, 1);
    expect(txt.overflow, TextOverflow.ellipsis);
  });

  testWidgets('filters by host and project', (t) async {
    feeds.lists['h1'] = [sess('a@web'), sess('b@api')];
    feeds.lists['h2'] = [sess('c@web')];
    await pump(t, [sampleHost(), sampleHost(id: 'h2', label: 'laptop')]);
    expect(find.text('a@web'), findsOneWidget);
    expect(find.text('c@web'), findsOneWidget);

    await t.tap(find.byKey(const Key('chip-Host-h2')));
    await t.pump();
    expect(find.text('c@web'), findsOneWidget);
    expect(find.text('a@web'), findsNothing);

    await t.tap(find.widgetWithText(ChoiceChip, 'All').first);
    await t.pump();
    await t.tap(find.byKey(const Key('chip-Project-api')));
    await t.pump();
    expect(find.text('b@api'), findsOneWidget);
    expect(find.text('a@web'), findsNothing);
  });

  testWidgets('offline host: rows greyed with host offline, banner', (t) async {
    feeds.lists['h1'] = [sess('a@p')];
    await pump(t, [sampleHost()]);
    feeds.last('h1').states$.add(SseState.reconnecting);
    await t.pump(const Duration(milliseconds: 10));
    await t.pump(const Duration(milliseconds: 10));
    expect(find.textContaining('host offline'), findsOneWidget);
    expect(find.byKey(const Key('offline-h1')), findsOneWidget);
    expect(
      find.bySemanticsLabel(RegExp('a@p, on box, Idle, host offline')),
      findsOneWidget,
    );
    expect(t.widget<Opacity>(find.byType(Opacity).first).opacity, lessThan(1));
  });

  testWidgets('tap opens the session route and clears attention', (t) async {
    feeds.lists['h1'] = [sess('codex@web', attention: true)];
    await pump(t, [sampleHost()]);
    await t.tap(find.text('codex@web'));
    await t.pumpAndSettle();
    expect(find.text('SESSION h1 codex@web'), findsOneWidget);
    router.pop();
    await t.pumpAndSettle();
    expect(find.byKey(const Key('attention-dot')), findsNothing);
  });

  testWidgets('pull to refresh refetches the list', (t) async {
    feeds.lists['h1'] = [sess('a@p')];
    await pump(t, [sampleHost()]);
    feeds.lists['h1'] = [sess('a@p'), sess('b@p')];
    await t.fling(find.text('a@p'), const Offset(0, 400), 1000);
    await t.pump();
    await t.pump(const Duration(seconds: 1));
    await t.pump(const Duration(seconds: 1));
    expect(find.text('b@p'), findsOneWidget);
  });

  test('relativeTime', () {
    final n = DateTime.utc(2026, 1, 1, 12);
    expect(relativeTime(n.subtract(const Duration(seconds: 10)), n), 'now');
    expect(relativeTime(n.subtract(const Duration(hours: 3)), n), '3h ago');
    expect(relativeTime(n.subtract(const Duration(days: 2)), n), '2d ago');
    expect(relativeTime(null, n), '');
  });
}
