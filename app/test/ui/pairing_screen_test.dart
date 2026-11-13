import 'package:agentd_app/api/agentd_api.dart';
import 'package:agentd_app/ui/pairing_screen.dart';
import 'package:material_ui/material_ui.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';

import '../support/fakes.dart';

const goodLink =
    'agentd://pair?v=1&u=https%3A%2F%2Fbox.example%3A7477&c=ABCD-EFGH-JKMN&h=mybox';

void main() {
  late TestEnv env;
  setUp(() => env = TestEnv());
  tearDown(() => env.dispose());

  Future<void> pump(
    WidgetTester t, {
    String? link,
    List<dynamic> hosts = const [],
    String? rehost,
  }) async {
    final router = GoRouter(
      routes: [
        GoRoute(path: '/', builder: (c, s) => const Text('HOME')),
        GoRoute(
          path: '/pair',
          builder: (c, s) => PairingScreen(initialLink: link, rehostId: rehost),
        ),
      ],
      initialLocation: '/pair',
    );
    await t.pumpWidget(
      ProviderScope(
        // ignore: argument_type_not_assignable
        overrides: widgetOverrides(env, hosts.cast()).cast(),
        child: MaterialApp.router(routerConfig: router),
      ),
    );
    await t.pumpAndSettle();
  }

  String textOf(WidgetTester t, String label) => t
      .widget<TextField>(find.widgetWithText(TextField, label))
      .controller!
      .text;

  testWidgets('pasting a link fills the fields', (t) async {
    await pump(t);
    await t.enterText(
      find.widgetWithText(TextField, 'Paste agentd://pair link'),
      goodLink,
    );
    await t.pump();
    expect(textOf(t, 'Host URL'), 'https://box.example:7477');
    expect(textOf(t, 'Pairing code'), 'ABCD-EFGH-JKMN');
  });

  testWidgets('deep link prefills; invalid input shows an error', (t) async {
    await pump(t, link: goodLink);
    expect(textOf(t, 'Host URL'), 'https://box.example:7477');
    await t.enterText(find.widgetWithText(TextField, 'Pairing code'), 'bad');
    await t.tap(find.widgetWithText(FilledButton, 'Pair'));
    await t.pump();
    expect(find.textContaining('12 characters'), findsOneWidget);
    expect(env.gateways.pairCalls, isEmpty);
  });

  testWidgets('http URL to a remote host is rejected', (t) async {
    await pump(t);
    await t.enterText(
      find.widgetWithText(TextField, 'Host URL'),
      'http://box.example',
    );
    await t.enterText(
      find.widgetWithText(TextField, 'Pairing code'),
      'ABCD-EFGH-JKMN',
    );
    await t.tap(find.widgetWithText(FilledButton, 'Pair'));
    await t.pump();
    expect(find.textContaining('only allowed for localhost'), findsOneWidget);
  });

  testWidgets('confirm, wait with cancel, then fail with a friendly message', (
    t,
  ) async {
    await pump(t, link: goodLink);
    await t.tap(find.widgetWithText(FilledButton, 'Pair'));
    await t.pumpAndSettle();
    expect(find.text('Pair with this host?'), findsOneWidget);
    expect(find.textContaining('box.example'), findsWidgets);
    await t.tap(find.widgetWithText(FilledButton, 'Pair').last);
    await t.pump(const Duration(milliseconds: 500));
    await t.pump(const Duration(milliseconds: 500));
    expect(find.text('Waiting for approval on mybox…'), findsOneWidget);
    expect(env.gateways.pairCalls.single.code, 'ABCD-EFGH-JKMN');

    env.gateways.pairResult.completeError(
      ApiException(statusCode: 403, code: 'pairing_denied', message: 'x'),
    );
    await t.pump();
    await t.pump();
    expect(find.text('The host refused this device.'), findsOneWidget);
  });

  testWidgets('cancel returns to the form', (t) async {
    await pump(t, link: goodLink);
    await t.tap(find.widgetWithText(FilledButton, 'Pair'));
    await t.pumpAndSettle();
    await t.tap(find.widgetWithText(FilledButton, 'Pair').last);
    await t.pump(const Duration(milliseconds: 500));
    await t.pump(const Duration(milliseconds: 500));
    await t.tap(find.text('Cancel'));
    await t.pump();
    expect(find.textContaining('Cancelled'), findsOneWidget);
    expect(find.text('Waiting for approval on mybox…'), findsNothing);
  });

  testWidgets('success navigates home', (t) async {
    await pump(t, link: goodLink);
    await t.tap(find.widgetWithText(FilledButton, 'Pair'));
    await t.pumpAndSettle();
    await t.tap(find.widgetWithText(FilledButton, 'Pair').last);
    await t.pump(const Duration(milliseconds: 500));
    await t.pump(const Duration(milliseconds: 500));
    env.gateways.pairResult.complete(
      const PairResult(token: 't', deviceId: 'd', scope: Scope.write),
    );
    await t.pumpAndSettle();
    expect(find.text('HOME'), findsOneWidget);
  });

  group('re-pair', () {
    final pin = 'sha256:${'ab' * 32}';

    testWidgets('keeps URL and pin: no warning when nothing changed', (
      t,
    ) async {
      await pump(
        t,
        hosts: [sampleHost(pin: pin)],
        rehost: 'h1',
      );
      expect(textOf(t, 'Host URL'), 'https://box.example:7477');
      expect(textOf(t, 'Certificate fingerprint (optional)'), pin);
      await t.enterText(
        find.widgetWithText(TextField, 'Pairing code'),
        'ABCD-EFGH-JKMN',
      );
      await t.tap(find.widgetWithText(FilledButton, 'Pair'));
      await t.pumpAndSettle();
      await t.tap(find.widgetWithText(FilledButton, 'Pair').last);
      await t.pump(const Duration(milliseconds: 500));
      expect(find.byKey(const Key('endpoint-change-dialog')), findsNothing);
      expect(env.gateways.pairCalls.length, 1);
    });

    testWidgets(
      'dropping the pin warns with old vs new; cancel sends nothing',
      (t) async {
        await pump(
          t,
          hosts: [sampleHost(pin: pin)],
          rehost: 'h1',
        );
        await t.enterText(
          find.widgetWithText(TextField, 'Certificate fingerprint (optional)'),
          '',
        );
        await t.enterText(
          find.widgetWithText(TextField, 'Pairing code'),
          'ABCD-EFGH-JKMN',
        );
        await t.tap(find.widgetWithText(FilledButton, 'Pair'));
        await t.pumpAndSettle();
        await t.tap(find.widgetWithText(FilledButton, 'Pair').last);
        await t.pumpAndSettle();
        expect(find.byKey(const Key('endpoint-change-dialog')), findsOneWidget);
        expect(find.text('Current fingerprint: $pin'), findsOneWidget);
        expect(
          find.text('New fingerprint: none (system trust)'),
          findsOneWidget,
        );
        expect(
          find.textContaining('removes certificate pinning'),
          findsOneWidget,
        );
        await t.tap(find.text('Cancel').last);
        await t.pumpAndSettle();
        expect(env.gateways.pairCalls, isEmpty);
      },
    );

    testWidgets('a confirmed URL change goes ahead', (t) async {
      await pump(
        t,
        hosts: [sampleHost(pin: pin)],
        rehost: 'h1',
      );
      await t.enterText(
        find.widgetWithText(TextField, 'Host URL'),
        'https://moved.example:7477',
      );
      await t.enterText(
        find.widgetWithText(TextField, 'Pairing code'),
        'ABCD-EFGH-JKMN',
      );
      await t.tap(find.widgetWithText(FilledButton, 'Pair'));
      await t.pumpAndSettle();
      await t.tap(find.widgetWithText(FilledButton, 'Pair').last);
      await t.pumpAndSettle();
      expect(
        find.text('Current URL: https://box.example:7477'),
        findsOneWidget,
      );
      expect(find.text('New URL: https://moved.example:7477'), findsOneWidget);
      await t.tap(find.byKey(const Key('confirm-endpoint-change')));
      await t.pump(const Duration(milliseconds: 500));
      expect(env.gateways.pairCalls.length, 1);
    });
  });
}
