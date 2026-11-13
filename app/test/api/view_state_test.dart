import 'dart:convert';
import 'dart:math';

import 'package:agentd_app/api/agentd_api.dart';
import 'package:test/test.dart';

typedef J = Map<String, dynamic>;

/// Dart mirror of the gateway's `diffViews`: one splice on the lines (after
/// dropping the head that scrolled out) plus the changed scalar fields.
J diff(J oldV, J newV) {
  final oldFirst = oldV['first_line'] as int;
  final newFirst = newV['first_line'] as int;
  final oldLines = (oldV['lines'] as List).cast<J>();
  final newLines = (newV['lines'] as List).cast<J>();
  final drop = min(max(newFirst - oldFirst, 0), oldLines.length);
  final kept = oldLines.sublist(drop);
  var p = 0;
  while (p < kept.length &&
      p < newLines.length &&
      jsonEncode(kept[p]) == jsonEncode(newLines[p])) {
    p++;
  }
  var s = 0;
  while (s < kept.length - p &&
      s < newLines.length - p &&
      jsonEncode(kept[kept.length - 1 - s]) ==
          jsonEncode(newLines[newLines.length - 1 - s])) {
    s++;
  }
  final patch = <String, dynamic>{
    'from_revision': oldV['revision'],
    'revision': newV['revision'],
    'epoch': newV['epoch'],
    'first_line': newFirst,
  };
  if (kept.length - p - s > 0 || newLines.length - p - s > 0) {
    patch['splice'] = {
      'at': p,
      'delete': kept.length - p - s,
      'insert': newLines.sublist(p, newLines.length - s),
    };
  }
  final set = <String, dynamic>{};
  for (final k in const [
    'status',
    'status_reason',
    'attention',
    'title',
    'cwd',
    'input',
    'options',
    'hints',
    'dialog',
    'idle_ms',
    'exit_code',
  ]) {
    if (jsonEncode(oldV[k]) != jsonEncode(newV[k])) set[k] = newV[k];
  }
  if (set.isNotEmpty) patch['set'] = set;
  return patch;
}

const _statuses = [
  'starting',
  'working',
  'idle',
  'waiting',
  'exited',
  'sleepy',
];
const _roles = ['transcript', 'status', 'dialog', 'gap', 'novel'];

J randomLine(Random r) => {
  'text': 'line ${r.nextInt(6)}',
  'role': _roles[r.nextInt(_roles.length)],
};

/// Next revision of [v]: scroll the head, edit/append/delete lines near the
/// tail and change scalar fields at random. Optional fields come and go.
J mutate(Random r, J v) {
  final n = <String, dynamic>{...v};
  n['revision'] = (v['revision'] as int) + 1 + r.nextInt(3);
  var lines = [...(v['lines'] as List).cast<J>()];
  var first = v['first_line'] as int;
  if (r.nextInt(4) == 0 && lines.isNotEmpty) {
    final k = 1 + r.nextInt(min(3, lines.length));
    lines = lines.sublist(k);
    first += k;
  }
  switch (r.nextInt(5)) {
    case 0: // append
      lines.addAll([for (var i = 0; i < 1 + r.nextInt(3); i++) randomLine(r)]);
    case 1: // rewrite the last lines
      final k = min(lines.length, 1 + r.nextInt(3));
      lines = [
        ...lines.sublist(0, lines.length - k),
        for (var i = 0; i < r.nextInt(4); i++) randomLine(r),
      ];
    case 2: // edit in the middle
      if (lines.isNotEmpty) lines[r.nextInt(lines.length)] = randomLine(r);
    case 3: // truncate everything
      if (r.nextInt(6) == 0) lines = [];
    default:
      break;
  }
  n['lines'] = lines;
  n['first_line'] = first;
  if (r.nextBool()) n['status'] = _statuses[r.nextInt(_statuses.length)];
  if (r.nextInt(3) == 0) {
    n['attention'] = r.nextBool()
        ? {
            'set': true,
            'reason': 'waiting',
            'since': '2026-11-13T09:0${r.nextInt(9)}:00Z',
          }
        : {'set': false};
  }
  if (r.nextInt(4) == 0) {
    r.nextBool() ? n['title'] = 't${r.nextInt(3)}' : n.remove('title');
  }
  if (r.nextInt(6) == 0) {
    r.nextBool() ? n['cwd'] = '/w${r.nextInt(3)}' : n.remove('cwd');
  }
  if (r.nextInt(3) == 0) {
    n['input'] = {
      'text': r.nextBool() ? '' : 'draft ${r.nextInt(5)}',
      'ghost': r.nextBool() ? '' : 'ghost',
      'state': const ['empty', 'draft', 'suggestion'][r.nextInt(3)],
    };
  }
  if (r.nextInt(3) == 0) {
    n['options'] = [
      for (var i = 0; i < r.nextInt(4); i++)
        {
          'id': '$i',
          'label': 'opt ${r.nextInt(5)}',
          'index': i,
          'kind': 'numbered',
          'selected': r.nextBool(),
          'confidence': r.nextInt(10) / 10,
        },
    ];
  }
  if (r.nextInt(5) == 0) {
    n['hints'] = r.nextBool()
        ? [
            {'key': 'esc', 'action': 'cancel'},
          ]
        : <J>[];
  }
  if (r.nextInt(4) == 0) {
    r.nextBool()
        ? n['dialog'] = {'question': 'q${r.nextInt(3)}?'}
        : n.remove('dialog');
  }
  if (r.nextBool()) n['idle_ms'] = r.nextInt(5000);
  if (r.nextInt(8) == 0) {
    r.nextBool() ? n['exit_code'] = r.nextInt(3) : n.remove('exit_code');
  }
  return n;
}

J baseView(Random r) => {
  'name': 'codex@webapp',
  'epoch': 'e1',
  'revision': 1,
  'status': 'idle',
  'first_line': r.nextInt(50),
  'lines': [for (var i = 0; i < r.nextInt(8); i++) randomLine(r)],
  'input': {'text': '', 'ghost': '', 'state': 'empty'},
  'options': <J>[],
  'hints': <J>[],
  'attention': {'set': false},
  'idle_ms': 0,
};

SseEvent ev(String name, J data) =>
    SseEvent(id: 'x', event: name, data: jsonEncode(data));

void main() {
  test('patches applied in sequence equal fresh views (property test)', () {
    for (var seed = 0; seed < 300; seed++) {
      final r = Random(seed);
      var cur = baseView(r);
      final st = ViewState();
      expect(st.handle(ev('view', cur)), PatchOutcome.applied);
      for (var step = 0; step < 30; step++) {
        final next = mutate(r, cur);
        // Go through JSON like the wire does.
        final patch =
            jsonDecode(jsonEncode(diff(cur, next))) as Map<String, dynamic>;
        final out = st.handle(ev('view.patch', patch));
        expect(out, PatchOutcome.applied, reason: 'seed $seed step $step');
        expect(
          st.view!.toJson(),
          View.fromJson(next).toJson(),
          reason: 'seed $seed step $step patch ${jsonEncode(patch)}',
        );
        cur = next;
      }
    }
  });

  test('first_line drops head lines before the splice is applied', () {
    final st = ViewState(
      View.fromJson({
        'name': 'a',
        'epoch': 'e',
        'revision': 1,
        'first_line': 10,
        'lines': [
          {'text': 'l10'},
          {'text': 'l11'},
          {'text': 'l12'},
        ],
      }),
    );
    final out = st.applyPatch(
      ViewStreamPatch.fromJson({
        'from_revision': 1,
        'revision': 2,
        'epoch': 'e',
        'first_line': 12,
        'splice': {
          'at': 1,
          'delete': 0,
          'insert': [
            {'text': 'l13'},
          ],
        },
      }),
    );
    expect(out, PatchOutcome.applied);
    expect(st.view!.firstLine, 12);
    expect([for (final l in st.view!.lines) l.text], ['l12', 'l13']);
    expect(st.view!.revision, 2);
  });

  test('set null clears dialog and exit_code', () {
    final st = ViewState(
      View.fromJson({
        'name': 'a',
        'epoch': 'e',
        'revision': 1,
        'dialog': {'question': 'q'},
        'exit_code': 1,
      }),
    );
    st.applyPatch(
      ViewStreamPatch.fromJson({
        'from_revision': 1,
        'revision': 2,
        'epoch': 'e',
        'first_line': 0,
        'set': {'dialog': null, 'exit_code': null},
      }),
    );
    expect(st.view!.dialog, isNull);
    expect(st.view!.exitCode, isNull);
  });

  test('wrong base, wrong epoch, bad splice and junk need a refetch', () {
    View v() => View.fromJson({
      'name': 'a',
      'epoch': 'e',
      'revision': 5,
      'first_line': 3,
      'lines': [
        {'text': 'x'},
      ],
    });
    final st = ViewState(v());
    ViewStreamPatch p(J over) => ViewStreamPatch.fromJson({
      'from_revision': 5,
      'revision': 6,
      'epoch': 'e',
      'first_line': 3,
      ...over,
    });
    expect(st.applyPatch(p({'from_revision': 4})), PatchOutcome.needsRefetch);
    expect(st.applyPatch(p({'epoch': 'other'})), PatchOutcome.needsRefetch);
    expect(st.applyPatch(p({'first_line': 2})), PatchOutcome.needsRefetch);
    expect(
      st.applyPatch(
        p({
          'splice': {'at': 2, 'delete': 0, 'insert': <J>[]},
        }),
      ),
      PatchOutcome.needsRefetch,
    );
    expect(
      st.handle(const SseEvent(event: 'view.patch', data: 'not json')),
      PatchOutcome.needsRefetch,
    );
    // Nothing above touched the state.
    expect(st.view!.toJson(), v().toJson());
    expect(ViewState().applyPatch(p({})), PatchOutcome.needsRefetch);
    expect(
      st.handle(const SseEvent(event: 'status.changed', data: '{}')),
      isNull,
    );
  });
}
