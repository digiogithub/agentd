import 'package:agentd_app/state/providers.dart';
import 'package:agentd_app/ui/hosts_screen.dart';
import 'package:material_ui/material_ui.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';

import '../support/fakes.dart';

Future<void> pumpHosts(
  WidgetTester t,
  TestEnv env,
  List<dynamic> hosts, {
  Map<String, HostStatus> status = const {},
}) async {
  await t.pumpWidget(
    ProviderScope(
      // ignore: argument_type_not_assignable
      overrides: widgetOverrides(env, hosts.cast(), status: status).cast(),
      child: const MaterialApp(home: HostsScreen()),
    ),
  );
  await t.pump();
  await t.pump();
}

void main() {
  late TestEnv env;
  setUp(() => env = TestEnv());
  tearDown(() => env.dispose());

  testWidgets('empty state', (t) async {
    await pumpHosts(t, env, []);
    expect(find.text('No hosts paired'), findsOneWidget);
    expect(find.text('Pair host'), findsOneWidget);
  });

  testWidgets('lists hosts with online and offline status', (t) async {
    await pumpHosts(
      t,
      env,
      [sampleHost(), sampleHost(id: 'h2', label: 'laptop')],
      status: {'h2': HostStatus.offline},
    );
    expect(find.text('box'), findsOneWidget);
    expect(find.text('laptop'), findsOneWidget);
    expect(find.textContaining('Online'), findsOneWidget);
    expect(find.textContaining('Offline'), findsOneWidget);
    expect(find.bySemanticsLabel(RegExp('laptop, Offline')), findsOneWidget);
  });

  testWidgets('rename updates the label', (t) async {
    await pumpHosts(t, env, [sampleHost()]);
    await t.tap(find.byType(PopupMenuButton<String>));
    await t.pumpAndSettle();
    await t.tap(find.text('Rename'));
    await t.pumpAndSettle();
    await t.enterText(find.byType(TextField), 'workstation');
    await t.tap(find.text('Save'));
    await t.pumpAndSettle();
    expect(find.text('workstation'), findsOneWidget);
    expect(find.text('box'), findsNothing);
  });

  testWidgets('remove shows the device id and revoke command', (t) async {
    await pumpHosts(t, env, [sampleHost()]);
    await t.tap(find.byType(PopupMenuButton<String>));
    await t.pumpAndSettle();
    await t.tap(find.text('Remove'));
    await t.pumpAndSettle();
    expect(find.text('agentd serve devices revoke dev_h1'), findsOneWidget);
    expect(find.textContaining('Device id: dev_h1'), findsOneWidget);
    await t.tap(find.widgetWithText(FilledButton, 'Remove'));
    await t.pumpAndSettle();
    expect(find.text('No hosts paired'), findsOneWidget);
  });

  testWidgets('host menu has Launch agent for that host', (t) async {
    final router = GoRouter(
      routes: [
        GoRoute(path: '/', builder: (c, s) => const HostsScreen()),
        GoRoute(
          path: '/hosts/:id/launch',
          builder: (c, s) => Text('LAUNCH ${s.pathParameters['id']}'),
        ),
      ],
    );
    await t.pumpWidget(
      ProviderScope(
        // ignore: argument_type_not_assignable
        overrides: widgetOverrides(env, [
          sampleHost(),
          sampleHost(id: 'h2', label: 'laptop'),
        ]).cast(),
        child: MaterialApp.router(routerConfig: router),
      ),
    );
    await t.pump();
    await t.pump();
    await t.tap(find.byType(PopupMenuButton<String>).last);
    await t.pumpAndSettle();
    await t.tap(find.text('Launch agent'));
    await t.pumpAndSettle();
    expect(find.text('LAUNCH h2'), findsOneWidget);
  });
}
