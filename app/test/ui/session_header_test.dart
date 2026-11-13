import 'package:agentd_app/api/agentd_api.dart';
import 'package:agentd_app/state/providers.dart';
import 'package:agentd_app/state/session_view.dart';
import 'package:agentd_app/ui/launch/session_actions_menu.dart';
import 'package:agentd_app/ui/session/session_screen.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:go_router/go_router.dart';
import 'package:material_ui/material_ui.dart' hide View, Dialog;

import '../state/launch_support.dart';
import '../state/session_fakes.dart';
import '../support/fakes.dart';

Future<({FakeSessionGateway gw, GoRouter router, FakeClient client})> pump(
  WidgetTester t,
  View first, {
  FakeSessionGateway? gw,
}) async {
  final g = gw ?? FakeSessionGateway();
  final client = FakeClient();
  final router = GoRouter(
    initialLocation: '/s',
    routes: [
      GoRoute(
        path: '/s',
        builder: (c, s) => const SessionScreen(hostId: 'h1', session: 's'),
      ),
      GoRoute(
        path: '/mailbox/compose',
        builder: (c, s) => Text(
          'COMPOSE host=${s.uri.queryParameters['host']} '
          'to=${s.uri.queryParameters['to']}',
        ),
      ),
    ],
  );
  await t.pumpWidget(
    ProviderScope(
      overrides: [
        // ignore: argument_type_not_assignable
        hostsProvider.overrideWith(() => FakeHosts([sampleHost()])),
        // ignore: argument_type_not_assignable
        sessionGatewayProvider.overrideWith((ref, k) async => g),
        clients({'h1': client}),
      ],
      child: MaterialApp.router(routerConfig: router),
    ),
  );
  await t.pump();
  await t.pump();
  g.sendView(first);
  await t.pump();
  await t.pump();
  return (gw: g, router: router, client: client);
}

void main() {
  testWidgets('the host label is prominent next to the session name', (
    t,
  ) async {
    await pump(t, makeView());
    final label = t.widget<Text>(find.byKey(const Key('host-label')));
    expect(label.data, 'box');
    expect(label.style!.fontWeight, FontWeight.bold);
    expect(find.text('s'), findsOneWidget);
  });

  testWidgets('Raw screen opens a read-only monospace dump', (t) async {
    final r = await pump(t, makeView());
    r.gw.terminalText = 'line one\nline two';
    await t.tap(find.byKey(const Key('raw-screen-button')));
    await t.pumpAndSettle();
    final text = t.widget<SelectableText>(
      find.byKey(const Key('raw-screen-text')),
    );
    expect(text.data, 'line one\nline two');
    expect(text.style!.fontFamily, 'monospace');
    // Read-only: no field in the dialog, nothing was sent.
    expect(
      find.descendant(
        of: find.byKey(const Key('raw-screen')),
        matching: find.byType(TextField),
      ),
      findsNothing,
    );
    expect(r.gw.acts, isEmpty);
    await t.tap(find.text('Close'));
    await t.pumpAndSettle();
    expect(find.byKey(const Key('raw-screen')), findsNothing);
  });

  testWidgets('Send mail opens compose prefilled with host and session', (
    t,
  ) async {
    await pump(t, makeView());
    await t.tap(find.byKey(const Key('mail-session-button')));
    await t.pumpAndSettle();
    expect(find.text('COMPOSE host=h1 to=s'), findsOneWidget);
  });

  testWidgets('the actions menu offers kill and remove for a live session', (
    t,
  ) async {
    await pump(t, makeView());
    expect(find.byType(SessionActionsMenu), findsOneWidget);
    await t.tap(find.byTooltip('Session actions'));
    await t.pumpAndSettle();
    expect(find.text('Kill'), findsOneWidget);
    expect(find.text('Remove'), findsOneWidget);
  });

  testWidgets('an exited session only offers remove; kill goes to the host', (
    t,
  ) async {
    final r = await pump(t, makeView(status: Status.exited, exitCode: 0));
    await t.tap(find.byTooltip('Session actions'));
    await t.pumpAndSettle();
    expect(find.text('Kill'), findsNothing);
    expect(find.text('Remove'), findsOneWidget);
    await t.tap(find.text('Remove'));
    await t.pumpAndSettle();
    await t.tap(find.widgetWithText(FilledButton, 'Remove'));
    await t.pumpAndSettle();
    expect(r.client.removeCalls, [('s', false)]);
  });

  group('interrupt and option confirms', () {
    testWidgets('interrupt is not sent when the view moved on meanwhile', (
      t,
    ) async {
      final r = await pump(t, makeView(status: Status.working));
      await t.tap(find.byKey(const Key('key-interrupt')));
      await t.pump(const Duration(milliseconds: 400));
      // While the confirm dialog is open the screen changes.
      r.gw.sendView(makeView(revision: 2, status: Status.waiting));
      await t.pump();
      await t.tap(find.byKey(const Key('confirm-interrupt')));
      await t.pump(const Duration(milliseconds: 400));
      expect(r.gw.acts, isEmpty);
      expect(find.byKey(const Key('act-feedback')), findsOneWidget);
      expect(find.textContaining('review it again'), findsOneWidget);
    });

    testWidgets('interrupt carries the displayed revision when unchanged', (
      t,
    ) async {
      final r = await pump(t, makeView(revision: 7, status: Status.working));
      await t.tap(find.byKey(const Key('key-interrupt')));
      await t.pump(const Duration(milliseconds: 400));
      await t.tap(find.byKey(const Key('confirm-interrupt')));
      await t.pump(const Duration(milliseconds: 400));
      expect(r.gw.acts.single.req.action, ActAction.interrupt);
      expect(r.gw.acts.single.req.revision, 7);
      expect(r.gw.acts.single.req.epoch, 'e1');
    });

    testWidgets('a low-confidence option is not sent if the menu changed', (
      t,
    ) async {
      const low = Option(
        id: 'o9',
        label: 'Maybe',
        index: 0,
        kind: OptionKind.numbered,
        confidence: 0.4,
      );
      final r = await pump(
        t,
        makeView(status: Status.waiting, options: [low, optB]),
      );
      await t.tap(find.byKey(const Key('option-o9')));
      await t.pumpAndSettle();
      r.gw.sendView(
        makeView(revision: 2, status: Status.waiting, options: [optB]),
      );
      await t.pump();
      await t.tap(find.byKey(const Key('confirm-low-confidence')));
      await t.pumpAndSettle();
      expect(r.gw.acts, isEmpty);
      expect(find.byKey(const Key('review-again')), findsOneWidget);
    });

    testWidgets('a confirmed low-confidence option on the same view is sent', (
      t,
    ) async {
      const low = Option(
        id: 'o9',
        label: 'Maybe',
        index: 0,
        kind: OptionKind.numbered,
        confidence: 0.4,
      );
      final r = await pump(
        t,
        makeView(revision: 3, status: Status.waiting, options: [low]),
      );
      await t.tap(find.byKey(const Key('option-o9')));
      await t.pumpAndSettle();
      await t.tap(find.byKey(const Key('confirm-low-confidence')));
      await t.pumpAndSettle();
      expect(r.gw.acts.single.req.optionId, 'o9');
      expect(r.gw.acts.single.req.revision, 3);
    });
  });

  testWidgets('busy: banner, text kept, no auto retry; Retry clears it', (
    t,
  ) async {
    final g = FakeSessionGateway();
    g.actQueue.add(
      ApiException(statusCode: 409, code: 'busy', message: 'draft'),
    );
    await pump(t, makeView(), gw: g);
    await t.enterText(find.byKey(const Key('composer-field')), 'run tests');
    await t.pump();
    await t.tap(find.byKey(const Key('composer-send')));
    await t.pump();
    await t.pump();
    expect(
      find.text('Someone is typing on the host — try again when they finish.'),
      findsOneWidget,
    );
    final field = t.widget<TextField>(find.byKey(const Key('composer-field')));
    expect(field.controller!.text, 'run tests');
    // The host becomes free: nothing is sent on its own.
    g.sendView(makeView(revision: 2));
    await t.pump();
    await t.pump(const Duration(seconds: 1));
    expect(g.acts.length, 1);
    // Explicit Retry reuses the key and the composer does not stay primed.
    await t.tap(find.byKey(const Key('feedback-retry')));
    await t.pump();
    await t.pump();
    expect(g.acts.length, 2);
    expect(g.acts[1].key, g.acts[0].key);
    expect(
      t
          .widget<TextField>(find.byKey(const Key('composer-field')))
          .controller!
          .text,
      '',
    );
  });
}
