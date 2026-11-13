import 'package:agentd_app/api/agentd_api.dart';
import 'package:agentd_app/state/providers.dart';
import 'package:agentd_app/state/session_view.dart';
import 'package:agentd_app/ui/session/session_screen.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';
import 'package:material_ui/material_ui.dart' hide View, Dialog;

import '../state/session_fakes.dart';
import '../support/fakes.dart';

Future<FakeSessionGateway> pump(
  WidgetTester t,
  View first, {
  FakeSessionGateway? gw,
}) async {
  final g = gw ?? FakeSessionGateway();
  await t.pumpWidget(
    ProviderScope(
      overrides: [
        // ignore: argument_type_not_assignable
        hostsProvider.overrideWith(() => FakeHosts([sampleHost()])),
        // ignore: argument_type_not_assignable
        sessionGatewayProvider.overrideWith((ref, k) async => g),
      ],
      child: const MaterialApp(
        home: SessionScreen(hostId: 'h1', session: 's'),
      ),
    ),
  );
  await t.pump();
  await t.pump();
  g.sendView(first);
  await t.pump();
  await t.pump();
  return g;
}

TextField field(WidgetTester t) =>
    t.widget<TextField>(find.byKey(const Key('composer-field')));

void main() {
  testWidgets('shows header, transcript and enabled composer', (t) async {
    await pump(t, makeView(lines: ['first line', 'second']));
    expect(find.text('s'), findsOneWidget);
    expect(find.text('box'), findsOneWidget);
    expect(find.text('idle'), findsOneWidget);
    expect(find.text('first line'), findsOneWidget);
    expect(field(t).enabled, true);
  });

  testWidgets('send types the prompt and clears the field', (t) async {
    final g = await pump(t, makeView());
    await t.enterText(find.byKey(const Key('composer-field')), 'run tests');
    await t.pump();
    await t.tap(find.byKey(const Key('composer-send')));
    await t.pump();
    await t.pump();
    expect(g.acts.single.req.action, ActAction.prompt);
    expect(g.acts.single.req.text, 'run tests');
    expect(field(t).controller!.text, '');
  });

  testWidgets('ghost is a hint only and is never sent implicitly', (t) async {
    final g = await pump(
      t,
      makeView(
        input: const Input(
          ghost: 'Try "fix lint"',
          state: InputState.suggestion,
          stateRaw: 'suggestion',
        ),
      ),
    );
    expect(field(t).decoration!.hintText, 'Try "fix lint"');
    expect(field(t).controller!.text, '');
    // Send is disabled with an empty field.
    await t.tap(find.byKey(const Key('composer-send')), warnIfMissed: false);
    await t.pump();
    expect(g.acts, isEmpty);
    // Using it only copies it into the field.
    await t.tap(find.byKey(const Key('use-suggestion')));
    await t.pump();
    expect(field(t).controller!.text, 'Try "fix lint"');
    expect(g.acts, isEmpty);
  });

  testWidgets('host draft disables the composer with the reason', (t) async {
    await pump(
      t,
      makeView(
        input: const Input(
          text: 'rm -r',
          state: InputState.draft,
          stateRaw: 'draft',
        ),
      ),
    );
    expect(field(t).enabled, false);
    expect(find.text('Someone is typing on the host.'), findsOneWidget);
    expect(find.textContaining('rm -r'), findsOneWidget);
  });

  testWidgets('working disables composer; interrupt needs confirm', (t) async {
    final g = await pump(t, makeView(status: Status.working));
    expect(field(t).enabled, false);
    expect(find.byKey(const Key('composer-reason')), findsOneWidget);
    await t.tap(find.byKey(const Key('key-interrupt')));
    await t.pump();
    expect(g.acts, isEmpty);
    await t.tap(find.byKey(const Key('confirm-interrupt')));
    await t.pump();
    await t.pump();
    expect(g.acts.single.req.action, ActAction.interrupt);
  });

  testWidgets('quick keys send abstract key names', (t) async {
    final g = await pump(t, makeView());
    for (final id in ['esc', 'up', 'down', 'enter', 'tab']) {
      await t.tap(find.byKey(Key('key-$id')));
      await t.pump();
      await t.pump();
    }
    expect(g.acts.map((a) => a.req.keys!.single), [
      'esc',
      'up',
      'down',
      'enter',
      'tab',
    ]);
  });

  testWidgets('option tap sends a full choose request', (t) async {
    final g = await pump(
      t,
      makeView(
        revision: 11,
        epoch: 'ep',
        status: Status.waiting,
        options: [optA, optB],
        dialog: const Dialog(question: 'Proceed?'),
      ),
    );
    expect(find.text('Proceed?'), findsOneWidget);
    expect(field(t).enabled, false); // options pending: answer above
    await t.tap(find.byKey(const Key('option-o2')));
    await t.pump();
    await t.pump();
    final r = g.acts.single.req;
    expect((r.optionId, r.label, r.revision, r.epoch), ('o2', 'No', 11, 'ep'));
  });

  testWidgets('low confidence option requires a confirm tap', (t) async {
    final g = await pump(
      t,
      makeView(
        status: Status.waiting,
        options: const [
          Option(id: 'x', label: 'Maybe', index: 1, confidence: 0.4),
        ],
      ),
    );
    await t.tap(find.byKey(const Key('option-x')));
    await t.pump();
    await t.pump();
    expect(g.acts, isEmpty);
    await t.tap(find.byKey(const Key('confirm-low-confidence')));
    await t.pump();
    await t.pump();
    expect(g.acts.single.req.optionId, 'x');
  });

  testWidgets('conflict replaces the view and shows review again', (t) async {
    final g = FakeSessionGateway();
    g.actQueue.add(
      ApiException(
        statusCode: 409,
        code: 'stale_revision',
        message: 'x',
        details: {
          'view': makeView(
            revision: 20,
            status: Status.waiting,
            options: [optB],
            lines: ['changed menu'],
          ).toJson(),
        },
      ),
    );
    await pump(
      t,
      makeView(status: Status.waiting, options: [optA, optB]),
      gw: g,
    );
    await t.tap(find.byKey(const Key('option-o1')));
    await t.pump();
    await t.pump();
    expect(find.byKey(const Key('option-o1')), findsNothing);
    expect(find.byKey(const Key('review-again')), findsOneWidget);
    expect(find.text('changed menu'), findsOneWidget);
    expect(g.acts.length, 1);
  });

  testWidgets('outcome_unknown: note with Send again, field cleared', (
    t,
  ) async {
    final g = FakeSessionGateway();
    g.actQueue.add(ApiException.outcomeUnknown('lost'));
    await pump(t, makeView(), gw: g);
    await t.enterText(find.byKey(const Key('composer-field')), 'hello');
    await t.pump();
    await t.tap(find.byKey(const Key('composer-send')));
    await t.pump();
    await t.pump();
    expect(find.textContaining('Result unknown'), findsOneWidget);
    expect(find.byKey(const Key('feedback-retry')), findsNothing);
    // Never a primed Send with the same text: it moved into the note.
    expect(field(t).controller!.text, '');
    expect(find.byKey(const Key('unknown-note')), findsOneWidget);
    expect(find.text('Sent? result unknown'), findsOneWidget);
    expect(
      find.descendant(
        of: find.byKey(const Key('unknown-note')),
        matching: find.text('hello'),
      ),
      findsOneWidget,
    );
    expect(g.acts.length, 1);
    await t.tap(find.byKey(const Key('unknown-send-again')));
    await t.pump();
    await t.pump();
    expect(g.acts.length, 2);
    expect(g.acts[1].key, isNot(g.acts[0].key));
    expect(g.acts[1].req.text, 'hello');
    expect(find.byKey(const Key('unknown-note')), findsNothing);
  });

  testWidgets('exited session is read-only with the exit code', (t) async {
    await pump(t, makeView(status: Status.exited, exitCode: 3));
    expect(find.text('exited 3'), findsOneWidget);
    expect(find.byKey(const Key('exited-banner')), findsOneWidget);
    expect(find.byKey(const Key('composer-field')), findsNothing);
    expect(find.byKey(const Key('key-esc')), findsNothing);
    expect(find.byKey(const Key('dialog-card')), findsNothing);
  });
}
