import 'package:agentd_app/api/agentd_api.dart';
import 'package:agentd_app/state/providers.dart';
import 'package:agentd_app/ui/launch/launch_screen.dart';
import 'package:agentd_app/ui/launch/session_actions_menu.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:material_ui/material_ui.dart';

import '../state/launch_support.dart';
import '../support/fakes.dart';

Future<void> pumpApp(
  WidgetTester t,
  FakeClient c,
  Widget Function() home,
) async {
  t.view.physicalSize = const Size(800, 1600);
  t.view.devicePixelRatio = 1;
  addTearDown(t.view.reset);
  final router = GoRouter(
    routes: [
      GoRoute(path: '/', builder: (_, _) => home()),
      GoRoute(
        path: '/hosts/:id/sessions/:name',
        builder: (_, s) => Text('SESSION ${s.pathParameters['name']}'),
      ),
    ],
  );
  addTearDown(router.dispose);
  await t.pumpWidget(
    ProviderScope(
      retry: (_, _) => null,
      overrides: [
        clients({'h1': c}),
        hostsProvider.overrideWith(() => FakeHosts([sampleHost()])),
      ],
      child: MaterialApp.router(routerConfig: router),
    ),
  );
  await t.pumpAndSettle();
}

void main() {
  testWidgets('validates project, previews default name, launches', (t) async {
    final c = FakeClient();
    await pumpApp(t, c, () => const LaunchScreen(hostId: 'h1'));
    expect(find.text('/home/jose/src/webapp'), findsOneWidget);

    await t.ensureVisible(find.byKey(const ValueKey('launch')));
    await t.tap(find.byKey(const ValueKey('launch')));
    await t.pumpAndSettle();
    expect(
      find.text('Pick a project or type an absolute path'),
      findsOneWidget,
    );
    expect(c.runCalls, isEmpty);

    await t.enterText(find.byKey(const ValueKey('target')), 'relative/dir');
    await t.tap(find.byKey(const ValueKey('launch')));
    await t.pumpAndSettle();
    expect(find.text('Not an alias: use an absolute path'), findsOneWidget);

    await t.tap(find.byKey(const ValueKey('alias-webapp')));
    await t.tap(find.byKey(const ValueKey('harness-codex')));
    await t.pumpAndSettle();
    expect(find.textContaining('codex@webapp (default)'), findsOneWidget);

    await t.tap(find.byKey(const ValueKey('launch')));
    await t.pumpAndSettle();
    expect(c.runCalls.single.req.target, 'webapp');
    expect(c.runCalls.single.req.harness, 'codex');
    expect(c.runCalls.single.req.name, isNull);
    expect(find.text('SESSION codex@webapp'), findsOneWidget);
  });

  testWidgets('hides admin-only harness field for non-admin', (t) async {
    await pumpApp(t, FakeClient(), () => const LaunchScreen(hostId: 'h1'));
    expect(find.byKey(const ValueKey('custom-harness')), findsNothing);
  });

  testWidgets('admin may type another harness', (t) async {
    final c = FakeClient()
      ..infoResult = const Info(
        host: 'box',
        scopes: [Scope.admin],
        harnesses: ['claude'],
      );
    await pumpApp(t, c, () => const LaunchScreen(hostId: 'h1'));
    await t.enterText(find.byKey(const ValueKey('custom-harness')), 'aider');
    await t.enterText(find.byKey(const ValueKey('target')), '/srv/x');
    await t.enterText(find.byKey(const ValueKey('name')), 'mine');
    await t.tap(find.byKey(const ValueKey('launch')));
    await t.pumpAndSettle();
    expect(c.runCalls.single.req.harness, 'aider');
    expect(c.runCalls.single.req.name, 'mine');
  });

  testWidgets('read-only device cannot launch', (t) async {
    final c = FakeClient()
      ..infoResult = const Info(
        host: 'box',
        scopes: [Scope.read],
        harnesses: ['claude'],
      );
    await pumpApp(t, c, () => const LaunchScreen(hostId: 'h1'));
    expect(find.text('Read-only device'), findsOneWidget);
    final b = t.widget<FilledButton>(find.byKey(const ValueKey('launch')));
    expect(b.onPressed, isNull);
  });

  testWidgets('friendly error and outcome_unknown refresh', (t) async {
    final c = FakeClient()..runError = apiErr(409, 'already_exists');
    await pumpApp(t, c, () => const LaunchScreen(hostId: 'h1'));
    await t.enterText(find.byKey(const ValueKey('target')), '/srv/x');
    await t.tap(find.byKey(const ValueKey('launch')));
    await t.pumpAndSettle();
    expect(find.textContaining('already exists'), findsOneWidget);

    c.runError = ApiException.outcomeUnknown('t');
    await t.tap(find.byKey(const ValueKey('launch')));
    await t.pumpAndSettle();
    expect(find.byKey(const ValueKey('check-outcome')), findsOneWidget);
    expect(find.byKey(const ValueKey('launch')), findsNothing);
    await t.ensureVisible(find.byKey(const ValueKey('check-outcome')));
    await t.tap(find.byKey(const ValueKey('check-outcome')));
    await t.pumpAndSettle();
    // Not listed (or not matching): "not visible yet", nothing cleared.
    expect(find.textContaining('Not visible yet'), findsOneWidget);
    expect(find.byKey(const ValueKey('check-outcome')), findsOneWidget);
    c.sessions = [
      SessionSummary(
        name: 'claude@x',
        cwd: '/srv/x',
        profile: 'claude',
        startedAt: DateTime.now().add(const Duration(seconds: 5)),
      ),
    ];
    await t.ensureVisible(find.byKey(const ValueKey('check-outcome')));
    await t.tap(find.byKey(const ValueKey('check-outcome')));
    await t.pumpAndSettle();
    expect(find.text('SESSION claude@x'), findsOneWidget);
    expect(c.runCalls.length, 2);
  });

  group('session actions menu', () {
    Future<void> open(WidgetTester t, FakeClient c, {bool running = true}) =>
        pumpApp(
          t,
          c,
          () => Scaffold(
            body: SessionActionsMenu(
              hostId: 'h1',
              hostLabel: 'box',
              session: 'codex@webapp',
              running: running,
            ),
          ),
        );

    testWidgets('kill asks, names session and host, sends chosen signal', (
      t,
    ) async {
      final c = FakeClient();
      await open(t, c);
      await t.tap(find.byType(PopupMenuButton<String>));
      await t.pumpAndSettle();
      await t.tap(find.text('Kill'));
      await t.pumpAndSettle();
      expect(
        find.textContaining('"codex@webapp" on host "box"'),
        findsOneWidget,
      );
      await t.tap(find.text('Cancel'));
      await t.pumpAndSettle();
      expect(c.killCalls, isEmpty);

      await t.tap(find.byType(PopupMenuButton<String>));
      await t.pumpAndSettle();
      await t.tap(find.text('Kill'));
      await t.pumpAndSettle();
      await t.tap(find.textContaining('KILL (force)'));
      await t.tap(find.widgetWithText(FilledButton, 'Kill'));
      await t.pumpAndSettle();
      expect(c.killCalls.single, ('codex@webapp', 'KILL'));
    });

    testWidgets('remove of a running session uses force after confirm', (
      t,
    ) async {
      final c = FakeClient();
      await open(t, c);
      await t.tap(find.byType(PopupMenuButton<String>));
      await t.pumpAndSettle();
      await t.tap(find.text('Remove'));
      await t.pumpAndSettle();
      expect(find.textContaining('still running'), findsOneWidget);
      await t.tap(find.text('Stop and remove'));
      await t.pumpAndSettle();
      expect(c.removeCalls.single, ('codex@webapp', true));
    });

    testWidgets('exited session: no kill item, remove without force', (
      t,
    ) async {
      final c = FakeClient();
      await open(t, c, running: false);
      await t.tap(find.byType(PopupMenuButton<String>));
      await t.pumpAndSettle();
      expect(find.text('Kill'), findsNothing);
      await t.tap(find.text('Remove'));
      await t.pumpAndSettle();
      await t.tap(find.widgetWithText(FilledButton, 'Remove'));
      await t.pumpAndSettle();
      expect(c.removeCalls.single, ('codex@webapp', false));
    });
  });

  testWidgets('dialogs can be called directly', (t) async {
    late BuildContext ctx;
    await t.pumpWidget(
      MaterialApp(
        home: Builder(
          builder: (c) {
            ctx = c;
            return const SizedBox();
          },
        ),
      ),
    );
    final f = showRemoveDialog(
      ctx,
      session: 's',
      hostLabel: 'h',
      running: false,
    );
    await t.pumpAndSettle();
    await t.tap(find.text('Cancel'));
    expect(await f, isFalse);
  });
}
