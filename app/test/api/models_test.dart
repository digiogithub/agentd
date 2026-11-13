import 'package:agentd_app/api/agentd_api.dart';
import 'package:test/test.dart';

void main() {
  group('enums', () {
    test('unknown wire values map to unknown, known ones round trip', () {
      expect(Status.parse('waiting'), Status.waiting);
      expect(Status.parse('hibernating'), Status.unknown);
      expect(Status.parse(null), Status.unknown);
      expect(Status.parse(42), Status.unknown);
      expect(AttentionReason.parse('turn_done'), AttentionReason.turnDone);
      expect(ActOutcome.parse('keys_sent'), ActOutcome.keysSent);
      expect(ActOutcome.parse('teleported'), ActOutcome.unknown);
      expect(Scope.parse('root'), Scope.unknown);
      expect(LineRole.parse('future'), LineRole.unknown);
      expect(OptionKind.parse('slider'), OptionKind.unknown);
      expect(MessageState.parse('archived'), MessageState.unknown);
      expect(SessionState.parse('zombie'), SessionState.unknown);
      // The empty string is never a valid wire value.
      expect(Status.parse(''), Status.unknown);
    });
  });

  group('View', () {
    final json = <String, dynamic>{
      'name': 'codex@webapp',
      'epoch': '1760000000-4242-a1b2c3',
      'revision': 57,
      'status': 'waiting',
      'status_reason': 'menu',
      'attention': {
        'set': true,
        'reason': 'waiting',
        'since': '2026-11-13T09:20:11Z',
      },
      'title': 'Fix tests',
      'cwd': '/home/u/webapp',
      'first_line': 120,
      'lines': [
        {'text': 'hello', 'role': 'transcript'},
        {'text': 'Proceed?', 'role': 'dialog'},
      ],
      'input': {'text': '', 'ghost': 'try x', 'state': 'suggestion'},
      'options': [
        {
          'id': '1',
          'label': 'Yes',
          'index': 1,
          'kind': 'numbered',
          'selected': true,
          'confidence': 0.92,
        },
        {'id': '2', 'label': 'No', 'kind': 'yesno', 'checked': false},
      ],
      'hints': [
        {'key': 'esc', 'action': 'cancel'},
      ],
      'dialog': {'question': 'Proceed?'},
      'idle_ms': 1800,
    };

    test('parses every field', () {
      final v = View.fromJson(json);
      expect(v.name, 'codex@webapp');
      expect(v.revision, 57);
      expect(v.status, Status.waiting);
      expect(v.attention.set, isTrue);
      expect(v.attention.reason, AttentionReason.waiting);
      expect(v.attention.since, DateTime.utc(2026, 11, 13, 9, 20, 11));
      expect(v.firstLine, 120);
      expect(v.endLine, 122);
      expect(v.lines[1].role, LineRole.dialog);
      expect(v.input.state, InputState.suggestion);
      expect(v.options[0].selected, isTrue);
      expect(v.options[0].confidence, closeTo(0.92, 1e-9));
      expect(v.options[1].checked, isFalse);
      expect(v.options[1].kind, OptionKind.yesno);
      expect(v.hints.single.key, 'esc');
      expect(v.dialog?.question, 'Proceed?');
      expect(v.exitCode, isNull);
    });

    test('toJson is stable: parse(toJson(v)).toJson() == v.toJson()', () {
      final v = View.fromJson(json);
      expect(View.fromJson(v.toJson()).toJson(), v.toJson());
    });

    test('unknown enum values and unknown fields are tolerated', () {
      final v = View.fromJson({
        ...json,
        'status': 'meditating',
        'brand_new_field': {'x': 1},
        'lines': [
          {'text': 'a', 'role': 'holographic', 'extra': true},
        ],
        'options': [
          {'id': 'x', 'label': 'X', 'kind': 'dial'},
        ],
        'attention': {'set': true, 'reason': 'telepathy'},
      });
      expect(v.status, Status.unknown);
      expect(v.statusRaw, 'meditating');
      expect(v.lines.single.role, LineRole.unknown);
      expect(v.lines.single.text, 'a');
      expect(v.options.single.kind, OptionKind.unknown);
      expect(v.attention.reason, AttentionReason.unknown);
      expect(v.attention.reasonRaw, 'telepathy');
    });

    test('missing and mistyped fields take defaults instead of throwing', () {
      final v = View.fromJson({
        'name': 5,
        'lines': 'nope',
        'options': [1, 'x', null],
        'revision': 'x',
        'input': <String>[],
      });
      expect(v.name, '');
      expect(v.lines, isEmpty);
      expect(v.options, isEmpty);
      expect(v.revision, 0);
      expect(v.input.state, InputState.empty);
      expect(View.fromJson(<String, dynamic>{}).status, Status.unknown);
    });

    test('exit_code 0 is kept distinct from absent', () {
      expect(View.fromJson({...json, 'exit_code': 0}).exitCode, 0);
      expect(View.fromJson(json).exitCode, isNull);
    });
  });

  group('other models', () {
    test('Info', () {
      final i = Info.fromJson({
        'host': 'box',
        'gateway': {'version': '0.1.0'},
        'daemon': {'version': '0.1.0', 'pid': 99, 'epoch': 'e1'},
        'identity': 'human:jose',
        'scopes': ['read', 'write', 'cosmic'],
        'harnesses': ['claude', 'codex'],
        'profiles': [
          {'name': 'claude', 'builtin': true},
        ],
        'aliases': [
          {'name': 'webapp', 'dir': '/srv/webapp', 'source': 'file'},
        ],
        'limits': {'max_body_kb': 256, 'prompt_max_chars': 16000},
        'api_version': '1.0',
        'features': ['events', 'mail', 'future_thing'],
        'whatever': 1,
      });
      expect(i.host, 'box');
      expect(i.gatewayVersion, '0.1.0');
      expect(i.daemon.pid, 99);
      expect(i.scopes, [Scope.read, Scope.write, Scope.unknown]);
      expect(i.canWrite, isTrue);
      expect(i.canAdmin, isFalse);
      expect(i.aliases.single.dir, '/srv/webapp');
      expect(i.limits.promptMaxChars, 16000);
      expect(i.apiVersion, '1.0');
      expect(i.hasFeature('mail'), isTrue);
    });

    test('SessionSummary', () {
      final s = SessionSummary.fromJson({
        'name': 'codex@webapp',
        'state': 'running',
        'pid': 12,
        'cwd': '/x',
        'cmd': ['codex'],
        'profile': 'codex',
        'started_at': '2026-11-13T09:00:00Z',
        'idle_ms': 5,
        'unread': 2,
        'attached': 1,
        'cols': 80,
        'rows': 24,
        'input_state': 'draft',
        'status': 'working',
        'attention': true,
        'revision': 7,
        'preview': 'running tests',
      });
      expect(s.state, SessionState.running);
      expect(s.inputState, InputState.draft);
      expect(s.status, Status.working);
      expect(s.attention, isTrue);
      expect(s.unread, 2);
      expect(s.exitCode, isNull);
      final odd = SessionSummary.fromJson({
        'name': 'a',
        'state': 'paused',
        'status': 'daydreaming',
      });
      expect(odd.state, SessionState.unknown);
      expect(odd.stateRaw, 'paused');
      expect(odd.status, Status.unknown);
      expect(odd.statusRaw, 'daydreaming');
    });

    test('ActRequest serialises only the relevant fields', () {
      expect(ActRequest.interrupt().toJson(), {'action': 'interrupt'});
      expect(
        ActRequest.choose('2', revision: 57, epoch: 'e', label: 'No').toJson(),
        {
          'action': 'choose',
          'revision': 57,
          'epoch': 'e',
          'option_id': '2',
          'label': 'No',
        },
      );
      expect(ActRequest.prompt('hi', submit: false).toJson(), {
        'action': 'prompt',
        'text': 'hi',
        'submit': false,
      });
      expect(ActRequest.keys(['esc']).toJson(), {
        'action': 'keys',
        'keys': ['esc'],
      });
    });

    test('ActResult with unknown result', () {
      final r = ActResult.fromJson({
        'action': 'prompt',
        'result': 'levitated',
        'revision': 3,
        'status': 'working',
      });
      expect(r.result, ActOutcome.unknown);
      expect(r.resultRaw, 'levitated');
      expect(r.status, Status.working);
    });

    test('Message and GatewayEvent', () {
      final m = Message.fromJson({
        'id': 4,
        'sender': 'codex@webapp',
        'recipient': 'human:jose',
        'subject': 's',
        'body': 'b',
        'state': 'delivered',
        'attempts': 1,
        'created_at': '2026-11-13T09:00:00Z',
        'reply_to': 2,
      });
      expect(m.state, MessageState.delivered);
      expect(m.replyTo, 2);
      expect(m.readAt, isNull);
      final e = GatewayEvent.fromJson({
        'type': 'status.changed',
        'session': 'a',
        'ts': '2026-11-13T09:20:11Z',
        'data': {'from': 'working', 'to': 'waiting', 'revision': 57},
      });
      expect(e.toStatus, Status.waiting);
      expect(e.revision, 57);
    });

    test('RunRequest drops unset fields; PairResult hides the token', () {
      expect(const RunRequest(target: 'webapp', harness: 'codex').toJson(), {
        'target': 'webapp',
        'harness': 'codex',
      });
      final p = PairResult.fromJson({
        'token': 'agd_X_secret',
        'device_id': 'd1',
        'scope': 'write',
      });
      expect(p.token, 'agd_X_secret');
      expect(p.toString(), isNot(contains('secret')));
    });
  });

  group('ApiException', () {
    test('parses the envelope with details and headers', () {
      final e = ApiException.fromResponse(
        409,
        {
          'error': {
            'code': 'stale_revision',
            'message': 'option moved',
            'retryable': false,
            'details': {
              'revision': 58,
              'view': {
                'name': 'a',
                'epoch': 'e',
                'revision': 58,
                'lines': <String>[],
              },
            },
          },
        },
        headers: {'etag': '"e1-i2-r58"', 'retry-after': '3'},
      );
      expect(e.code, 'stale_revision');
      expect(e.isConflict, isTrue);
      expect(e.freshView?.revision, 58);
      expect(e.etag, '"e1-i2-r58"');
      expect(e.retryAfter, const Duration(seconds: 3));
    });

    test('non-envelope bodies still produce a typed error', () {
      final e = ApiException.fromResponse(502, '<html>bad gateway</html>');
      expect(e.code, 'http_502');
      expect(e.isOutcomeUnknown, isFalse);
      expect(ApiException.outcomeUnknown('x').isOutcomeUnknown, isTrue);
    });
  });
}
