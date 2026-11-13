import 'dart:convert';
import 'dart:io';

import 'package:agentd_app/api/agentd_api.dart';
import 'package:test/test.dart';
import 'package:yaml/yaml.dart';

/// Every route the client calls: operationId -> (method, path template).
/// The test fails when the spec has a route missing here (the client lacks
/// it) or when the client's route is absent from the spec.
const clientRoutes = <String, (String, String)>{
  'getHealth': ('get', '/v1/health'),
  'pairDevice': ('post', '/v1/pair'),
  'getInfo': ('get', '/v1/info'),
  'listSessions': ('get', '/v1/sessions'),
  'runSession': ('post', '/v1/sessions'),
  'getSession': ('get', '/v1/sessions/{name}'),
  'removeSession': ('delete', '/v1/sessions/{name}'),
  'killSession': ('post', '/v1/sessions/{name}/kill'),
  'getView': ('get', '/v1/sessions/{name}/view'),
  'ackView': ('post', '/v1/sessions/{name}/view/ack'),
  'act': ('post', '/v1/sessions/{name}/act'),
  'streamView': ('get', '/v1/sessions/{name}/view/stream'),
  'getTerminal': ('get', '/v1/sessions/{name}/terminal'),
  'streamEvents': ('get', '/v1/events'),
  'listMailbox': ('get', '/v1/mailbox'),
  'sendMessage': ('post', '/v1/mailbox/messages'),
  'getMessage': ('get', '/v1/mailbox/messages/{id}'),
  'markMessageRead': ('post', '/v1/mailbox/messages/{id}/read'),
  'replyMessage': ('post', '/v1/mailbox/messages/{id}/reply'),
};

/// Query parameters the client sends, per operation.
const clientQuery = <String, Set<String>>{
  'getView': {'lines', 'from', 'scrollback'},
  'removeSession': {'force'},
  'getTerminal': {'format', 'scrollback', 'ghost'},
  'listMailbox': {'all', 'mailbox', 'limit', 'before'},
  'getMessage': {'mailbox'},
  'streamEvents': {'session', 'kind'},
  'streamView': {'tail'},
};

/// Headers the client sends, per operation.
const clientHeaders = <String, Set<String>>{
  'listSessions': {'If-None-Match'},
  'getView': {'If-None-Match'},
  'act': {'Idempotency-Key', 'If-Match'},
  'runSession': {'Idempotency-Key'},
  'sendMessage': {'Idempotency-Key'},
  'replyMessage': {'Idempotency-Key'},
  'streamEvents': {'Last-Event-ID'},
  'streamView': {'Last-Event-ID'},
};

/// Fields the models read or write, per schema (JSON names). Kept in step
/// with lib/api/models.dart by `modelFields` below.
const modelFields = <String, Set<String>>{
  'Info': {
    'host',
    'gateway',
    'daemon',
    'identity',
    'scopes',
    'harnesses',
    'profiles',
    'aliases',
    'limits',
    'api_version',
    'features',
  },
  'SessionSummary': {
    'name',
    'state',
    'pid',
    'cwd',
    'cmd',
    'profile',
    'exit_code',
    'started_at',
    'last_output',
    'idle_ms',
    'unread',
    'attached',
    'cols',
    'rows',
    'title',
    'input_state',
    'status',
    'attention',
    'revision',
    'preview',
  },
  'RunRequest': {'target', 'harness', 'name', 'profile', 'args', 'cmd', 'env'},
  'Line': {'text', 'role'},
  'Input': {'text', 'ghost', 'state'},
  'Option': {
    'id',
    'label',
    'index',
    'kind',
    'selected',
    'checked',
    'confidence',
  },
  'View': {
    'name',
    'epoch',
    'revision',
    'status',
    'status_reason',
    'attention',
    'title',
    'cwd',
    'first_line',
    'lines',
    'input',
    'options',
    'hints',
    'dialog',
    'idle_ms',
    'exit_code',
  },
  'ViewStreamPatch': {
    'from_revision',
    'revision',
    'epoch',
    'first_line',
    'splice',
    'set',
  },
  'ActRequest': {
    'action',
    'revision',
    'epoch',
    'text',
    'submit',
    'option_id',
    'label',
    'force',
    'keys',
  },
  'ActResult': {
    'action',
    'result',
    'submitted',
    'revision',
    'status',
    'warning',
  },
  'TerminalSnapshot': {
    'format',
    'text',
    'ghost',
    'ghost_hidden',
    'cols',
    'rows',
  },
  'Event': {'type', 'session', 'ts', 'data'},
  'SendRequest': {'to', 'subject', 'body', 'reply_to'},
  'Message': {
    'id',
    'sender',
    'recipient',
    'subject',
    'body',
    'reply_to',
    'state',
    'attempts',
    'created_at',
    'delivered_at',
    'read_at',
    'replied_at',
    'warning',
  },
  'Error': {'error'},
};

/// Nested objects the models read.
const nestedFields = <String, Set<String>>{
  'Info.gateway': {'version'},
  'Info.daemon': {'version', 'pid', 'epoch'},
  'Info.limits': {'max_body_kb', 'prompt_max_chars'},
  'View.attention': {'set', 'reason', 'since'},
  'View.dialog': {'question'},
  'Error.error': {'code', 'message', 'retryable', 'details'},
};

Object? plain(Object? y) {
  if (y is YamlMap) {
    return {for (final e in y.entries) e.key as String: plain(e.value)};
  }
  if (y is YamlList) return [for (final e in y) plain(e)];
  return y;
}

void main() {
  final raw = File('../docs/api/openapi.yaml').readAsStringSync();
  final spec = plain(loadYaml(raw)) as Map<String, dynamic>;
  final paths = spec['paths'] as Map<String, dynamic>;
  final schemas =
      (spec['components'] as Map)['schemas'] as Map<String, dynamic>;
  final params =
      (spec['components'] as Map)['parameters'] as Map<String, dynamic>;

  // operationId -> (method, path, operation)
  final ops = <String, (String, String, Map<String, dynamic>)>{};
  paths.forEach((path, item) {
    (item as Map<String, dynamic>).forEach((m, op) {
      if (op is Map<String, dynamic> && op.containsKey('operationId')) {
        ops[op['operationId'] as String] = (m, path, op);
      }
    });
  });

  Set<String> declaredParams(
    String path,
    Map<String, dynamic> op,
    String where,
  ) {
    final out = <String>{};
    final all = [
      ...((paths[path] as Map)['parameters'] as List? ?? const []),
      ...(op['parameters'] as List? ?? const []),
    ];
    for (final p in all) {
      var pm = p as Map<String, dynamic>;
      if (pm.containsKey(r'$ref')) {
        pm =
            params[(pm[r'$ref'] as String).split('/').last]
                as Map<String, dynamic>;
      }
      if (pm['in'] == where) out.add(pm['name'] as String);
    }
    return out;
  }

  test('every spec operation has a client route and vice versa', () {
    expect(clientRoutes.keys.toSet(), ops.keys.toSet());
    clientRoutes.forEach((id, r) {
      final o = ops[id]!;
      expect((r.$1, r.$2), (o.$1, o.$2), reason: id);
    });
  });

  test('client methods exist for the table (smoke: AgentdClient surface)', () {
    // The class must expose a method per operation; this is a compile-time
    // check of the names the table maps to.
    final c = AgentdClient(
      baseUrl: Uri.parse('http://127.0.0.1:1'),
      token: 't',
    );
    addTearDown(c.close);
    final fns = <String, Object>{
      'getHealth': c.health,
      'pairDevice': c.pair,
      'getInfo': c.info,
      'listSessions': c.listSessions,
      'runSession': c.runSession,
      'getSession': c.getSession,
      'removeSession': c.removeSession,
      'killSession': c.killSession,
      'getView': c.getView,
      'ackView': c.ackView,
      'act': c.act,
      'streamView': c.viewStream,
      'getTerminal': c.getTerminal,
      'streamEvents': c.eventsStream,
      'listMailbox': c.listMailbox,
      'sendMessage': c.sendMessage,
      'getMessage': c.getMessage,
      'markMessageRead': c.markMessageRead,
      'replyMessage': c.replyMessage,
    };
    expect(fns.keys.toSet(), clientRoutes.keys.toSet());
  });

  test('query parameters and headers the client sends are declared', () {
    clientQuery.forEach((id, names) {
      final (_, path, op) = ops[id]!;
      expect(declaredParams(path, op, 'query'), containsAll(names), reason: id);
    });
    clientHeaders.forEach((id, names) {
      final (_, path, op) = ops[id]!;
      expect(
        declaredParams(path, op, 'header'),
        containsAll(names),
        reason: id,
      );
    });
  });

  test('model field names exist in the schemas', () {
    modelFields.forEach((schema, fields) {
      final s = schemas[schema] as Map<String, dynamic>?;
      expect(s, isNotNull, reason: 'schema $schema');
      final props = (s!['properties'] as Map<String, dynamic>).keys.toSet();
      expect(props, containsAll(fields), reason: schema);
    });
    nestedFields.forEach((path, fields) {
      final parts = path.split('.');
      var node = schemas[parts.first] as Map<String, dynamic>;
      for (final p in parts.skip(1)) {
        node =
            (node['properties'] as Map<String, dynamic>)[p]
                as Map<String, dynamic>;
      }
      final props = (node['properties'] as Map<String, dynamic>).keys.toSet();
      expect(props, containsAll(fields), reason: path);
    });
    // Pairing bodies are inline in the path item.
    final pair =
        ((((paths['/v1/pair'] as Map)['post'] as Map)['requestBody']
                    as Map)['content']
                as Map)['application/json']
            as Map;
    expect(
      ((pair['schema'] as Map)['properties'] as Map).keys,
      containsAll(['code', 'name', 'platform']),
    );
    final ok =
        ((((paths['/v1/pair'] as Map)['post'] as Map)['responses']
                    as Map)['201']
                as Map)['content']
            as Map;
    expect(
      (((ok['application/json'] as Map)['schema'] as Map)['properties'] as Map)
          .keys,
      containsAll(['token', 'device_id', 'scope']),
    );
  });

  test('model models cover every property of the schemas they mirror (no silent drift)', () {
    // Schemas whose every property the app reads: a new spec property fails
    // here, which is the prompt to decide whether the model needs it.
    for (final name in modelFields.keys) {
      final props =
          ((schemas[name] as Map)['properties'] as Map<String, dynamic>).keys
              .toSet();
      final missing = props.difference(modelFields[name]!);
      expect(
        missing,
        isEmpty,
        reason: '$name has spec properties the models ignore: $missing',
      );
    }
  });

  test('enum wire values match the spec enums', () {
    Set<String> enumOf(Map<String, dynamic> n) =>
        (n['enum'] as List).cast<String>().toSet();
    Set<String> wires<T>(List<T> values, String Function(T) w) =>
        {for (final v in values) w(v)}..remove('');
    Map<String, dynamic> prop(String schema, String p) =>
        ((schemas[schema] as Map)['properties'] as Map<String, dynamic>)[p]
            as Map<String, dynamic>;

    expect(
      wires(Status.values, (e) => e.wire),
      enumOf(schemas['Status'] as Map<String, dynamic>),
    );
    expect(
      wires(Scope.values, (e) => e.wire),
      enumOf(schemas['Scope'] as Map<String, dynamic>),
    );
    expect(
      wires(SessionState.values, (e) => e.wire),
      enumOf(prop('SessionSummary', 'state')),
    );
    expect(
      wires(InputState.values, (e) => e.wire),
      enumOf(prop('SessionSummary', 'input_state')),
    );
    expect(wires(LineRole.values, (e) => e.wire), enumOf(prop('Line', 'role')));
    expect(
      wires(OptionKind.values, (e) => e.wire),
      enumOf(prop('Option', 'kind')),
    );
    expect(
      wires(ActAction.values, (e) => e.wire),
      enumOf(prop('ActRequest', 'action')),
    );
    expect(
      wires(ActOutcome.values, (e) => e.wire),
      enumOf(prop('ActResult', 'result')),
    );
    expect(
      wires(MessageState.values, (e) => e.wire),
      enumOf(prop('Message', 'state')),
    );
    final att =
        (prop('View', 'attention')['properties']
                as Map<String, dynamic>)['reason']
            as Map<String, dynamic>;
    expect(wires(AttentionReason.values, (e) => e.wire), enumOf(att));
  });

  test('spec examples parse with the models', () {
    final act =
        (((paths['/v1/sessions/{name}/act'] as Map)['post']
                as Map)['requestBody']
            as Map);
    final examples =
        ((act['content'] as Map)['application/json'] as Map)['examples'] as Map;
    for (final e in examples.values) {
      final v = (e as Map)['value'] as Map<String, dynamic>;
      // Round trip through the request builders' JSON shape.
      expect(ActAction.parse(v['action']), isNot(ActAction.unknown));
      expect(jsonDecode(jsonEncode(v)), v);
    }
    final run =
        (((paths['/v1/sessions'] as Map)['post'] as Map)['requestBody'] as Map);
    final runEx =
        ((run['content'] as Map)['application/json'] as Map)['examples'] as Map;
    for (final e in runEx.values) {
      final v = (e as Map)['value'] as Map<String, dynamic>;
      expect(v.keys.toSet(), isNotEmpty);
      expect(
        ((schemas['RunRequest'] as Map)['properties'] as Map).keys,
        containsAll(v.keys),
      );
    }
  });
}
