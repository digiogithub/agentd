import 'dart:async';
import 'dart:convert';

import 'package:agentd_app/api/agentd_api.dart';

import 'package:agentd_app/state/notifications.dart';
import 'package:agentd_app/state/session_view.dart';
import 'package:agentd_app/ui/app_shell.dart';
import 'package:agentd_app/ui/session/session_screen.dart';
import 'package:agentd_app/ui/settings/settings_screen.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:material_ui/material_ui.dart';

import '../state/session_fakes.dart';
import '../support/fakes.dart';
import '../support/notify_fakes.dart';

class _OneView extends FakeSessionGateway {
  @override
  Stream<SseEvent> viewEvents({void Function(SseState)? onState}) =>
      Stream.value(
        SseEvent(event: 'view', data: jsonEncode(makeView().toJson())),
      );
}

void main() {
  late TestEnv env;
  late FakeNotifier notifier;
  late FakeOpener opener;
  setUp(() {
    env = TestEnv();
    notifier = FakeNotifier();
    opener = FakeOpener();
  });
  tearDown(() => env.dispose());

  List<dynamic> overrides({bool android = false, bool desktop = false}) => [
    ...widgetOverrides(env, [sampleHost(), sampleHost(id: 'h2', label: 'two')]),
    sessionGatewayProvider.overrideWith((ref, k) async => _OneView()),
    localNotifierProvider.overrideWithValue(notifier),
    trayServiceProvider.overrideWithValue(FakeTray()),
    windowServiceProvider.overrideWithValue(FakeWindow()),
    linkOpenerProvider.overrideWithValue(opener),
    isAndroidProvider.overrideWithValue(android),
    isDesktopProvider.overrideWithValue(desktop),
    // Settings live in memory: no file IO under FakeAsync.
    notifySettingsStoreProvider.overrideWith(
      (ref) => Completer<NotifySettingsStore>().future,
    ),
  ];

  Future<void> pumpApp(
    WidgetTester t, {
    bool android = false,
    bool desktop = false,
  }) async {
    await t.pumpWidget(
      ProviderScope(
        // ignore: argument_type_not_assignable
        overrides: overrides(android: android, desktop: desktop).cast(),
        child: const AgentdApp(),
      ),
    );
    await t.pumpAndSettle();
  }

  Future<void> openSettings(WidgetTester t) async {
    await t.tap(find.byKey(const Key('agents-overflow')));
    await t.pumpAndSettle();
    await t.tap(find.byKey(const Key('menu-settings')));
    await t.pumpAndSettle();
  }

  testWidgets(
    'settings reachable from the agents overflow menu; toggles work',
    (t) async {
      await pumpApp(t, desktop: true);
      await openSettings(t);
      expect(find.byType(SettingsScreen), findsOneWidget);
      final c = ProviderScope.containerOf(
        t.element(find.byType(SettingsScreen)),
      );
      expect(c.read(notifySettingsProvider).includePreview, isFalse);
      expect(c.read(notifySettingsProvider).muted, isFalse);
      await t.tap(find.byKey(const Key('set-preview')));
      await t.tap(find.byKey(const Key('set-mute')));
      await t.tap(find.byKey(const Key('set-host-h2')));
      await t.tap(find.byKey(const Key('set-tray')));
      await t.pumpAndSettle();
      final s = c.read(notifySettingsProvider);
      expect(s.includePreview, isTrue);
      expect(s.muted, isTrue);
      expect(s.mutedHosts, {'h2'});
      expect(s.keepInTray, isFalse);
      expect(find.byKey(const Key('set-fgs')), findsNothing);
    },
  );

  testWidgets('ntfy help shows the config snippet per host and opens ntfy', (
    t,
  ) async {
    await pumpApp(t);
    await openSettings(t);
    await t.scrollUntilVisible(
      find.byKey(const Key('ntfy-host-h1')),
      200,
      scrollable: find.byType(Scrollable).first,
    );
    await t.ensureVisible(find.byKey(const Key('ntfy-host-h1')));
    await t.pumpAndSettle();
    await t.tap(find.byType(ExpansionTile).first);
    await t.pumpAndSettle();
    final snippet = t.widget<SelectableText>(find.byType(SelectableText));
    expect(snippet.data, contains('[api.notify]'));
    expect(snippet.data, contains('ntfy_token_env'));
    expect(snippet.data, contains('ntfy_url'));
    await t.ensureVisible(find.byKey(const Key('open-ntfy')));
    await t.pumpAndSettle();
    opener.result = false;
    await t.tap(find.byKey(const Key('open-ntfy')));
    await t.pumpAndSettle();
    expect(opener.opened.single.scheme, 'ntfy');
    expect(find.textContaining('ntfy app not found'), findsOneWidget);
  });

  testWidgets('Android: rationale then permission request; fgs disabled', (
    t,
  ) async {
    await pumpApp(t, android: true);
    await openSettings(t);
    expect(find.text('Notifications not allowed'), findsOneWidget);
    final fgs = t.widget<SwitchListTile>(find.byKey(const Key('set-fgs')));
    expect(fgs.onChanged, isNull);
    await t.tap(find.byKey(const Key('set-allow')));
    await t.pumpAndSettle();
    expect(find.text('Allow notifications?'), findsOneWidget);
    expect(notifier.requests, 0);
    await t.tap(find.byKey(const Key('rationale-continue')));
    await t.pumpAndSettle();
    expect(notifier.requests, 1);
    expect(find.text('Notifications allowed'), findsOneWidget);
  });

  testWidgets('a notification tap opens the session (host by id)', (t) async {
    await pumpApp(t);
    notifier.tapCtl.add('agentd://open?h=h2&s=codex%40web');
    await t.pumpAndSettle();
    expect(find.byType(SessionScreen), findsOneWidget);
  });
}
