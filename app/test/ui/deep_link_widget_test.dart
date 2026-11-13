import 'dart:convert';

import 'package:agentd_app/api/agentd_api.dart';
import 'package:agentd_app/repositories/host.dart';
import 'package:agentd_app/state/providers.dart';
import 'package:agentd_app/ui/app_shell.dart';
import 'package:agentd_app/state/session_view.dart';
import 'package:agentd_app/ui/session/session_screen.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:material_ui/material_ui.dart';

import '../state/session_fakes.dart';
import '../support/fakes.dart';

class _OneViewGateway extends FakeSessionGateway {
  @override
  Stream<SseEvent> viewEvents({void Function(SseState)? onState}) =>
      Stream.value(
        SseEvent(event: 'view', data: jsonEncode(makeView().toJson())),
      );
}

void main() {
  late TestEnv env;
  setUp(() => env = TestEnv());
  tearDown(() => env.dispose());

  Future<void> pump(
    WidgetTester t, {
    List<Host>? hosts,
    bool hostsFail = false,
  }) async {
    await t.pumpWidget(
      ProviderScope(
        // ignore: argument_type_not_assignable
        overrides: [
          ...widgetOverrides(
            env,
            hosts ?? [sampleHost()],
            failHosts: hostsFail,
          ).cast(),
          // ignore: argument_type_not_assignable
          sessionGatewayProvider.overrideWith(
            (ref, k) async => _OneViewGateway(),
          ),
        ],
        child: const AgentdApp(),
      ),
    );
    await t.pumpAndSettle();
  }

  testWidgets('open link navigates to the session route', (t) async {
    await pump(t);
    env.links.controller.add(Uri.parse('agentd://open?h=box&s=codex%40web'));
    await t.pumpAndSettle();
    expect(find.byType(SessionScreen), findsOneWidget);
  });

  testWidgets('pair link opens the pairing form prefilled', (t) async {
    await pump(t);
    env.links.controller.add(
      Uri.parse(
        'agentd://pair?v=1&u=https%3A%2F%2Fnew.example%3A7477&c=ABCD-EFGH-JKMN',
      ),
    );
    await t.pumpAndSettle();
    expect(find.text('Pair host'), findsWidgets);
    expect(find.text('https://new.example:7477'), findsOneWidget);
  });

  testWidgets('unknown host shows a toast and stays', (t) async {
    await pump(t);
    env.links.controller.add(Uri.parse('agentd://open?h=ghost&s=a'));
    await t.pumpAndSettle();
    expect(find.text('Unknown host "ghost".'), findsOneWidget);
    expect(find.text('agentd'), findsOneWidget);
  });

  testWidgets('cold-start link is handled', (t) async {
    env.links.initialLink = Uri.parse('agentd://open?h=box&s=a');
    await pump(t);
    expect(find.byType(SessionScreen), findsOneWidget);
  });

  testWidgets('the same link delivered twice opens one screen', (t) async {
    env.links.initialLink = Uri.parse('agentd://open?h=box&s=a');
    await pump(t);
    // app_links also reports the launching link on the stream.
    env.links.controller.add(Uri.parse('agentd://open?h=box&s=a'));
    await t.pumpAndSettle();
    expect(find.byType(SessionScreen), findsOneWidget);
    // Back from the session lands on the agents list, not on a second copy.
    await t.pageBack();
    await t.pumpAndSettle();
    expect(find.byType(SessionScreen), findsNothing);
    expect(find.text('agentd'), findsOneWidget);
  });

  testWidgets('a label shared by two hosts opens a picker', (t) async {
    await pump(
      t,
      hosts: [
        sampleHost(label: 'Box'),
        sampleHost(id: 'h2', label: 'box', hostname: 'two.local'),
      ],
    );
    env.links.controller.add(Uri.parse('agentd://open?h=box&s=a'));
    await t.pumpAndSettle();
    expect(find.byType(SessionScreen), findsNothing);
    expect(find.textContaining('on which host?'), findsOneWidget);
    await t.tap(find.byKey(const Key('pick-host-h2')));
    await t.pumpAndSettle();
    expect(find.byType(SessionScreen), findsOneWidget);
    expect(t.widget<SessionScreen>(find.byType(SessionScreen)).hostId, 'h2');
  });

  testWidgets('a host id in the link wins over duplicate labels', (t) async {
    await pump(
      t,
      hosts: [
        sampleHost(label: 'box'),
        sampleHost(id: 'h2', label: 'box', hostname: 'two.local'),
      ],
    );
    env.links.controller.add(Uri.parse('agentd://open?h=h2&s=a'));
    await t.pumpAndSettle();
    expect(find.textContaining('on which host?'), findsNothing);
    expect(t.widget<SessionScreen>(find.byType(SessionScreen)).hostId, 'h2');
  });

  testWidgets('an unreadable hosts store shows an error, no crash', (t) async {
    await pump(t, hostsFail: true);
    env.links.controller.add(Uri.parse('agentd://open?h=box&s=a'));
    await t.pumpAndSettle();
    expect(find.textContaining('Cannot read the paired hosts'), findsOneWidget);
    expect(find.byType(SessionScreen), findsNothing);
  });

  testWidgets('the pairing code never travels in the route', (t) async {
    await pump(t);
    env.links.controller.add(
      Uri.parse(
        'agentd://pair?v=1&u=https%3A%2F%2Fnew.example%3A7477&c=ABCD-EFGH-JKMN',
      ),
    );
    await t.pumpAndSettle();
    final router =
        t.widget<MaterialApp>(find.byType(MaterialApp)).routerConfig!
            as GoRouter;
    final loc = router.routerDelegate.currentConfiguration.last.matchedLocation;
    expect(loc, '/pair');
    expect(loc, isNot(contains('ABCD')));
    expect(find.text('ABCD-EFGH-JKMN'), findsOneWidget); // prefilled
    // Consumed: opening the form again does not resurrect the link.
    expect(
      ProviderScope.containerOf(t.element(find.byType(AgentdApp)))
          .read(pendingPairLinkProvider)
          .take(),
      isNull,
    );
  });
}
