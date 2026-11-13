import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:agentd_app/api/agentd_api.dart';
import 'package:test/test.dart';

class Seen {
  Seen(this.method, this.path, this.query, this.headers, this.body);
  final String method;
  final String path;
  final Map<String, String> query;
  final HttpHeaders headers;
  final String body;
}

typedef Handler = FutureOr<void> Function(HttpRequest req, Seen seen);

Future<(HttpServer, List<Seen>)> stub(Handler h) async {
  final server = await HttpServer.bind(InternetAddress.loopbackIPv4, 0);
  final seen = <Seen>[];
  server.listen((req) async {
    final body = await utf8.decodeStream(req);
    final s = Seen(
      req.method,
      req.uri.path,
      req.uri.queryParameters,
      req.headers,
      body,
    );
    seen.add(s);
    await h(req, s);
  });
  return (server, seen);
}

void json(
  HttpRequest req,
  int status,
  Object body, {
  Map<String, String> headers = const {},
}) {
  req.response.statusCode = status;
  req.response.headers.contentType = ContentType.json;
  headers.forEach(req.response.headers.set);
  req.response.write(jsonEncode(body));
  req.response.close();
}

AgentdClient clientFor(HttpServer s) => AgentdClient(
  baseUrl: Uri.parse('http://127.0.0.1:${s.port}'),
  token: 'agd_TESTTOKEN_secret',
  requestTimeout: const Duration(seconds: 3),
);

final viewJson = {
  'name': 'codex@webapp',
  'epoch': 'e1',
  'revision': 5,
  'status': 'idle',
  'first_line': 0,
  'lines': [
    {'text': 'hi', 'role': 'transcript'},
  ],
};

void main() {
  late HttpServer server;
  late List<Seen> seen;
  late AgentdClient client;
  late Handler handler;

  setUp(() async {
    handler = (req, _) => json(req, 404, {
      'error': {'code': 'not_found', 'message': 'no route'},
    });
    final r = await stub((req, s) => handler(req, s));
    server = r.$1;
    seen = r.$2;
    client = clientFor(server);
  });
  tearDown(() async {
    client.close();
    await server.close(force: true);
  });

  test('health needs no token; info sends the bearer header only', () async {
    handler = (req, s) => s.path == '/v1/health'
        ? json(req, 200, {'ok': true})
        : json(req, 200, {
            'host': 'box',
            'gateway': {'version': '1'},
            'daemon': {'version': '1', 'pid': 1, 'epoch': 'e'},
            'identity': 'human:u',
            'scopes': ['write'],
            'harnesses': <String>[],
            'profiles': <String>[],
            'aliases': <String>[],
          });
    expect(await client.health(), isTrue);
    expect(seen.last.headers.value('authorization'), isNull);
    final info = await client.info();
    expect(info.host, 'box');
    expect(
      seen.last.headers.value('authorization'),
      'Bearer agd_TESTTOKEN_secret',
    );
    expect(seen.last.query, isEmpty); // never a token in the query string
  });

  test('view: ETag, If-None-Match and 304', () async {
    handler = (req, s) {
      if (s.headers.value('if-none-match') == '"e1-i2-r5"') {
        req.response.statusCode = 304;
        req.response.headers.set('ETag', '"e1-i2-r5"');
        req.response.close();
        return;
      }
      json(req, 200, viewJson, headers: {'ETag': '"e1-i2-r5"'});
    };
    final first = await client.getView('codex@webapp', lines: 50);
    expect(first, isA<ViewFull>());
    final full = first as ViewFull;
    expect(full.etag, '"e1-i2-r5"');
    expect(full.view.revision, 5);
    expect(seen.last.path, '/v1/sessions/codex@webapp/view');
    expect(seen.last.query, {'lines': '50'});

    final second = await client.getView('codex@webapp', ifNoneMatch: full.etag);
    expect(second, isA<ViewNotModified>());
    expect((second as ViewNotModified).etag, '"e1-i2-r5"');
  });

  test('view: unchanged marker and patch responses are accepted', () async {
    var n = 0;
    handler = (req, _) => n++ == 0
        ? json(req, 200, {'unchanged': true, 'revision': 5, 'epoch': 'e1'})
        : json(req, 200, {'patch': true, 'base': 5, 'revision': 6});
    final a = await client.getView('a');
    expect((a as ViewNotModified).revision, 5);
    final b = await client.getView('a');
    expect((b as ViewPatched).base, 5);
  });

  test('session names are percent-encoded as path segments', () async {
    handler = (req, _) => json(req, 200, {'name': 'a b/c', 'state': 'running'});
    await client.getSession('a b/c');
    expect(seen.last.path, '/v1/sessions/a%20b%2Fc');
    expect(seen.last.method, 'GET');
  });

  test('sessions list: ETag then 304', () async {
    handler = (req, s) {
      if (s.headers.value('if-none-match') == 'W/"abc"') {
        req.response.statusCode = 304;
        req.response.close();
        return;
      }
      json(
        req,
        200,
        {
          'sessions': [
            {'name': 'a', 'state': 'running'},
          ],
        },
        headers: {'ETag': 'W/"abc"'},
      );
    };
    final l = await client.listSessions();
    expect(l.sessions.single.name, 'a');
    expect(l.etag, 'W/"abc"');
    final l2 = await client.listSessions(ifNoneMatch: l.etag);
    expect(l2.notModified, isTrue);
    expect(l2.etag, 'W/"abc"');
  });

  test('act always carries an Idempotency-Key (UUIDv4) and If-Match', () async {
    handler = (req, _) => json(
      req,
      200,
      {
        'action': 'choose',
        'result': 'selected',
        'revision': 6,
        'status': 'working',
      },
      headers: {'Idempotent-Replayed': 'true'},
    );
    final r = await client.act(
      'a',
      ActRequest.choose('2', revision: 5, epoch: 'e1', label: 'No'),
      ifMatch: '"e1-i2-r5"',
    );
    expect(r.result, ActOutcome.selected);
    expect(r.replayed, isTrue);
    final s = seen.last;
    expect(s.method, 'POST');
    expect(s.path, '/v1/sessions/a/act');
    expect(s.headers.value('if-match'), '"e1-i2-r5"');
    expect(s.headers.contentType?.mimeType, 'application/json');
    expect(
      s.headers.value('idempotency-key'),
      matches(
        RegExp(
          r'^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$',
        ),
      ),
    );
    expect(jsonDecode(s.body), containsPair('option_id', '2'));

    await client.act('a', ActRequest.interrupt());
    final k1 = seen.last.headers.value('idempotency-key');
    expect(seen.last.headers.value('if-match'), isNull);
    await client.act('a', ActRequest.interrupt());
    expect(seen.last.headers.value('idempotency-key'), isNot(k1));
    await client.act('a', ActRequest.interrupt(), idempotencyKey: 'retry-1');
    await client.act('a', ActRequest.interrupt(), idempotencyKey: 'retry-1');
    expect(seen[seen.length - 2].headers.value('idempotency-key'), 'retry-1');
    expect(seen.last.headers.value('idempotency-key'), 'retry-1');
  });

  test('run, send and reply also send idempotency keys', () async {
    handler = (req, s) =>
        json(req, 201, {'name': 'x', 'id': 1, 'state': 'queued'});
    await client.runSession(
      const RunRequest(target: 'webapp', harness: 'codex'),
    );
    await client.sendMessage(const SendRequest(to: 'human:u', body: 'b'));
    await client.replyMessage(1, 'ok');
    expect(seen, hasLength(3));
    for (final s in seen) {
      expect(s.headers.value('idempotency-key'), isNotNull, reason: s.path);
    }
    expect(seen[0].path, '/v1/sessions');
    expect(seen[1].path, '/v1/mailbox/messages');
    expect(seen[2].path, '/v1/mailbox/messages/1/reply');
  });

  test('error envelope maps to ApiException', () async {
    handler = (req, _) => json(
      req,
      409,
      {
        'error': {
          'code': 'human_draft',
          'message': 'a terminal user is typing',
          'retryable': true,
          'details': {'since': 12},
        },
      },
      headers: {'Retry-After': '2'},
    );
    await expectLater(
      client.act('a', ActRequest.keys(['esc'])),
      throwsA(
        isA<ApiException>()
            .having((e) => e.code, 'code', 'human_draft')
            .having((e) => e.statusCode, 'status', 409)
            .having((e) => e.retryable, 'retryable', isTrue)
            .having(
              (e) => e.retryAfter,
              'retryAfter',
              const Duration(seconds: 2),
            )
            .having((e) => e.details['since'], 'details', 12)
            .having((e) => e.isConflict, 'isConflict', isTrue),
      ),
    );
  });

  test('conflict carries the fresh view and its ETag', () async {
    handler = (req, _) => json(
      req,
      409,
      {
        'error': {
          'code': 'stale_revision',
          'message': 'moved',
          'details': {
            'revision': 6,
            'view': {...viewJson, 'revision': 6},
          },
        },
      },
      headers: {'ETag': '"e1-i2-r6"'},
    );
    try {
      await client.act('a', ActRequest.choose('1', revision: 5, epoch: 'e1'));
      fail('expected conflict');
    } on ApiException catch (e) {
      expect(e.code, 'stale_revision');
      expect(e.freshView?.revision, 6);
      expect(e.etag, '"e1-i2-r6"');
    }
  });

  test('412 precondition_failed and 401 are typed', () async {
    handler = (req, s) => s.path.endsWith('/act')
        ? json(req, 412, {
            'error': {'code': 'precondition_failed', 'message': 'x'},
          })
        : json(req, 401, {
            'error': {'code': 'unauthenticated', 'message': 'bad token'},
          });
    await expectLater(
      client.act('a', ActRequest.interrupt(), ifMatch: '*'),
      throwsA(
        isA<ApiException>().having(
          (e) => e.isPreconditionFailed,
          '412',
          isTrue,
        ),
      ),
    );
    await expectLater(
      client.info(),
      throwsA(
        isA<ApiException>().having((e) => e.isUnauthenticated, '401', isTrue),
      ),
    );
  });

  test('gateway outcome_unknown (502) is surfaced as is', () async {
    handler = (req, _) => json(req, 502, {
      'error': {
        'code': 'outcome_unknown',
        'message': 'lost',
        'retryable': false,
        'details': {'view': viewJson},
      },
    });
    await expectLater(
      client.act('a', ActRequest.prompt('x')),
      throwsA(
        isA<ApiException>()
            .having((e) => e.isOutcomeUnknown, 'unknown', isTrue)
            .having((e) => e.retryable, 'retryable', isFalse)
            .having((e) => e.freshView?.revision, 'view', 5),
      ),
    );
  });

  test(
    'connection dropped after a write was sent is outcome_unknown',
    () async {
      handler = (req, _) async {
        final socket = await req.response.detachSocket(writeHeaders: false);
        socket.destroy();
      };
      await expectLater(
        client.act('a', ActRequest.prompt('x')),
        throwsA(
          isA<ApiException>().having(
            (e) => e.isOutcomeUnknown,
            'unknown',
            isTrue,
          ),
        ),
      );
    },
  );

  test('a dropped read is a plain ConnectionException', () async {
    handler = (req, _) async {
      final socket = await req.response.detachSocket(writeHeaders: false);
      socket.destroy();
    };
    await expectLater(client.info(), throwsA(isA<ConnectionException>()));
  });

  test('connection refused before sending is a ConnectionException, even for writes', () async {
    final port = server.port;
    await server.close(force: true);
    final c = AgentdClient(
      baseUrl: Uri.parse('http://127.0.0.1:$port'),
      token: 't',
    );
    await expectLater(
      c.act('a', ActRequest.interrupt()),
      throwsA(isA<ConnectionException>()),
    );
    c.close();
    server = await HttpServer.bind(
      InternetAddress.loopbackIPv4,
      0,
    ); // for tearDown
  });

  test('request timeout', () async {
    handler = (req, _) async {}; // never answers
    final c = AgentdClient(
      baseUrl: Uri.parse('http://127.0.0.1:${server.port}'),
      token: 't',
      requestTimeout: const Duration(milliseconds: 200),
    );
    await expectLater(c.info(), throwsA(isA<ConnectionException>()));
    c.close();
  });

  test('terminal, mailbox, kill, remove, ack, pair', () async {
    handler = (req, s) {
      switch ((s.method, s.path)) {
        case ('GET', '/v1/sessions/a/terminal'):
          json(req, 200, {
            'format': 'text',
            'text': 'screen',
            'cols': 80,
            'rows': 24,
          });
        case ('GET', '/v1/mailbox'):
          json(req, 200, {
            'messages': [
              {'id': 9, 'state': 'queued'},
            ],
            'next_before': 9,
          });
        case ('GET', '/v1/mailbox/messages/9'):
          json(req, 200, {'id': 9, 'state': 'delivered'});
        case ('POST', '/v1/mailbox/messages/9/read'):
          json(req, 200, {'id': 9, 'state': 'read'});
        case ('POST', '/v1/sessions/a/kill'):
          json(req, 202, {'name': 'a', 'state': 'running'});
        case ('DELETE', '/v1/sessions/a'):
          req.response.statusCode = 204;
          req.response.close();
        case ('POST', '/v1/sessions/a/view/ack'):
          json(req, 200, {'revision': 12});
        case ('POST', '/v1/pair'):
          json(req, 201, {
            'token': 'agd_I_s',
            'device_id': 'd',
            'scope': 'write',
          });
        default:
          json(req, 404, {
            'error': {'code': 'not_found', 'message': s.path},
          });
      }
    };
    final t = await client.getTerminal(
      'a',
      format: 'ansi',
      scrollback: true,
      ghost: true,
    );
    expect(t.text, 'screen');
    expect(seen.last.query, {
      'format': 'ansi',
      'scrollback': 'true',
      'ghost': 'true',
    });
    final page = await client.listMailbox(
      all: true,
      limit: 10,
      before: 50,
      mailbox: 'human:u',
    );
    expect(page.messages.single.id, 9);
    expect(page.nextBefore, 9);
    expect(seen.last.query, {
      'all': 'true',
      'limit': '10',
      'before': '50',
      'mailbox': 'human:u',
    });
    expect((await client.getMessage(9)).state, MessageState.delivered);
    expect((await client.markMessageRead(9)).state, MessageState.read);
    await client.killSession('a', signal: 'INT');
    expect(jsonDecode(seen.last.body), {'signal': 'INT'});
    await client.removeSession('a', force: true);
    expect(seen.last.query, {'force': 'true'});
    expect(await client.ackView('a'), 12);
    final p = await client.pair(
      code: 'ABCD-EFGH-JKMN',
      name: 'laptop',
      platform: 'linux',
    );
    expect(p.token, 'agd_I_s');
    expect(seen.last.headers.value('authorization'), isNull); // unauthenticated
    expect(jsonDecode(seen.last.body), {
      'code': 'ABCD-EFGH-JKMN',
      'name': 'laptop',
      'platform': 'linux',
    });
  });

  test('plain http is refused except loopback; a pin needs https', () {
    expect(
      () => AgentdClient(
        baseUrl: Uri.parse('http://box.example:7477'),
        token: 't',
      ),
      throwsArgumentError,
    );
    expect(
      () => AgentdClient(
        baseUrl: Uri.parse('http://10.0.2.2:7477'),
        token: 't',
      ).close(),
      returnsNormally,
    );
    expect(
      () => AgentdClient(
        baseUrl: Uri.parse('http://127.0.0.1:1'),
        token: 't',
        pin: SpkiPin.parse('sha256:${'ab' * 32}'),
      ),
      throwsArgumentError,
    );
    expect(
      () => AgentdClient(baseUrl: Uri.parse('ftp://x'), token: 't'),
      throwsArgumentError,
    );
  });

  test('SSE over the real client: resume with Last-Event-ID and auth header', () async {
    var n = 0;
    handler = (req, s) async {
      req.response.bufferOutput = false;
      req.response.headers.contentType = ContentType('text', 'event-stream');
      if (n++ == 0) {
        req.response.write(
          'retry: 20\n\nevent: hello\ndata: {"type":"hello"}\n\n'
          'id: 1:1\nevent: status.changed\ndata: {"type":"status.changed","session":"a"}\n\n',
        );
        await req.response.flush();
        await Future<void>.delayed(const Duration(milliseconds: 30));
        await req.response.close(); // drop
      } else {
        req.response.write(
          'id: 1:1\nevent: status.changed\ndata: {}\n\n'
          'id: 1:2\nevent: session.exited\ndata: {"type":"session.exited"}\n\n',
        );
        await req.response.flush();
      }
    };
    final sse = client.eventsStream(
      sessions: ['a', 'b'],
      kinds: ['status.changed', 'session.*'],
    );
    final got = <String>[];
    final sub = sse.events().listen((e) => got.add('${e.event}@${e.id}'));
    final sw = Stopwatch()..start();
    while (got.length < 3 && sw.elapsed < const Duration(seconds: 5)) {
      await Future<void>.delayed(const Duration(milliseconds: 20));
    }
    await sub.cancel();
    expect(got, ['hello@null', 'status.changed@1:1', 'session.exited@1:2']);
    expect(seen[0].headers.value('last-event-id'), isNull);
    expect(seen[1].headers.value('last-event-id'), '1:1');
    expect(
      seen[0].headers.value('authorization'),
      'Bearer agd_TESTTOKEN_secret',
    );
    expect(seen[0].headers.value('accept'), 'text/event-stream');
    expect(seen[0].path, '/v1/events');
    expect(seen[0].query, {
      'session': 'a,b',
      'kind': 'status.changed,session.*',
    });
  });

  test('SSE 401 surfaces as ApiException and stops', () async {
    handler = (req, _) => json(req, 401, {
      'error': {'code': 'unauthenticated', 'message': 'revoked'},
    });
    final errs = <Object>[];
    final done = Completer<void>();
    client
        .viewStream('a', tail: 50)
        .events()
        .listen((_) {}, onError: errs.add, onDone: done.complete);
    await done.future.timeout(const Duration(seconds: 3));
    expect(errs.single, isA<ApiException>());
    expect(seen.single.path, '/v1/sessions/a/view/stream');
    expect(seen.single.query, {'tail': '50'});
  });

  test('view stream + ViewState follow a session', () async {
    handler = (req, s) async {
      req.response.bufferOutput = false;
      req.response.headers.contentType = ContentType('text', 'event-stream');
      req.response.write(
        'id: e1-i2-r5\nevent: view\ndata: ${jsonEncode(viewJson)}\n\n'
        'id: e1-i2-r6\nevent: view.patch\ndata: ${jsonEncode({
          'from_revision': 5,
          'revision': 6,
          'epoch': 'e1',
          'first_line': 0,
          'splice': {
            'at': 1,
            'delete': 0,
            'insert': [
              {'text': 'more'},
            ],
          },
          'set': {'status': 'working'},
        })}\n\n',
      );
      await req.response.flush();
    };
    final st = ViewState();
    final c = Completer<void>();
    final sub = client.viewStream('a').events().listen((e) {
      st.handle(e);
      if (st.view?.revision == 6 && !c.isCompleted) c.complete();
    });
    await c.future.timeout(const Duration(seconds: 3));
    await sub.cancel();
    expect([for (final l in st.view!.lines) l.text], ['hi', 'more']);
    expect(st.view!.status, Status.working);
  });
}
