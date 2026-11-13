/// Typed REST client of the agentd gateway (`agentd serve`), on `dart:io`
/// `HttpClient` so TLS pinning, REST and SSE share one trust decision
/// (frontend-app.md sections 4.1 and 4.2). Pure Dart: no Flutter imports.
library;

import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:isolate';

import 'package:uuid/uuid.dart';

import 'errors.dart';
import 'json.dart';
import 'loopback.dart';
import 'models.dart';
import 'spki.dart';
import 'sse.dart';

/// Payloads above this are JSON-decoded in a background isolate.
const _isolateDecodeThreshold = 64 * 1024;

class _Resp {
  _Resp(this.status, this.headers, this.body);
  final int status;
  final Map<String, String> headers;
  final String body;

  Future<Object?> json() async {
    if (body.isEmpty) return null;
    try {
      if (body.length > _isolateDecodeThreshold) {
        final b = body;
        return await Isolate.run(() => jsonDecode(b));
      }
      return jsonDecode(body);
    } on FormatException {
      return null;
    }
  }
}

/// The bearer token. Its [toString] never reveals the secret.
class _Token {
  const _Token(this._value);
  final String _value;
  @override
  String toString() => '***';
}

class AgentdClient {
  /// [baseUrl] is the gateway root (`https://host:7477`). [pin] is the
  /// certificate's SPKI hash; when set it is the only trust anchor (system
  /// roots are not consulted); when null the system roots decide and
  /// `badCertificateCallback` is never set. Plain `http://` is refused except
  /// for loopback / Android-emulator hosts unless [allowInsecureHttp].
  AgentdClient({
    required Uri baseUrl,
    required String token,
    SpkiPin? pin,
    this.connectTimeout = const Duration(seconds: 10),
    this.requestTimeout = const Duration(seconds: 30),
    bool allowInsecureHttp = false,
    Uuid? uuid,
  }) : baseUrl = baseUrl,
       _token = _Token(token),
       pin = pin,
       _uuid = uuid ?? const Uuid() {
    if (baseUrl.scheme != 'https') {
      if (baseUrl.scheme != 'http') {
        throw ArgumentError.value(baseUrl, 'baseUrl', 'must be http(s)');
      }
      if (pin != null) {
        throw ArgumentError('a pin needs an https base URL');
      }
      if (!allowInsecureHttp && !_isLoopbackHost(baseUrl.host)) {
        throw ArgumentError.value(
          baseUrl,
          'baseUrl',
          'plain http is only allowed for loopback hosts',
        );
      }
    }
    _http = _makeHttpClient();
  }

  final Uri baseUrl;
  final SpkiPin? pin;
  final Duration connectTimeout;
  final Duration requestTimeout;
  final _Token _token;
  final Uuid _uuid;
  late final HttpClient _http;
  SpkiPin? _rejected;

  static bool _isLoopbackHost(String host) => isLoopbackHost(host);

  HttpClient _makeHttpClient() {
    final expected = pin;
    final HttpClient c;
    if (expected == null) {
      c = HttpClient();
    } else {
      // Nothing is trusted by default, so every handshake reaches the
      // callback and the pin is the only trust anchor.
      c = HttpClient(context: SecurityContext(withTrustedRoots: false));
      c.badCertificateCallback = (cert, host, port) {
        final SpkiPin seen;
        try {
          seen = SpkiPin.ofCertificate(cert.der);
        } on FormatException {
          return false;
        }
        if (expected.matches(seen)) return true;
        _rejected = seen;
        return false;
      };
    }
    c.connectionTimeout = connectTimeout;
    c.userAgent = 'agentd-app';
    return c;
  }

  /// Closes idle and in-flight connections.
  void close() => _http.close(force: true);

  // ------------------------------------------------------------ transport

  Uri _uri(List<String> segments, [Map<String, String>? query]) {
    final base = baseUrl.pathSegments.where((s) => s.isNotEmpty);
    return baseUrl.replace(
      pathSegments: [...base, ...segments],
      queryParameters: query == null || query.isEmpty ? null : query,
    );
  }

  Never _transportError(
    Object e, {
    required bool sent,
    required bool sideEffect,
  }) {
    final rejected = _rejected;
    if (rejected != null && !sent) {
      _rejected = null;
      throw PinMismatchException(expected: pin!.text, seen: rejected.text);
    }
    if (sent && sideEffect) throw ApiException.outcomeUnknown(e);
    throw ConnectionException(
      e is TimeoutException ? 'timed out' : e.toString(),
      e,
    );
  }

  Future<_Resp> _request(
    String method,
    List<String> segments, {
    Map<String, String>? query,
    Object? body,
    Map<String, String>? headers,
    bool auth = true,
    bool sideEffect = false,
    Duration? timeout,
  }) async {
    final uri = _uri(segments, query);
    final limit = timeout ?? requestTimeout;
    _rejected = null;
    HttpClientRequest req;
    try {
      req = await _http.openUrl(method, uri).timeout(limit);
    } on Object catch (e) {
      _transportError(e, sent: false, sideEffect: sideEffect);
    }
    var sent = false;
    try {
      req.followRedirects = false;
      req.headers.set(HttpHeaders.acceptHeader, 'application/json');
      if (auth) {
        req.headers.set(
          HttpHeaders.authorizationHeader,
          'Bearer ${_token._value}',
        );
      }
      headers?.forEach(req.headers.set);
      if (body != null) {
        req.headers.contentType = ContentType(
          'application',
          'json',
          charset: 'utf-8',
        );
        req.add(utf8.encode(jsonEncode(body)));
      }
      sent = true;
      final res = await req.close().timeout(limit);
      final text = await res.transform(utf8.decoder).join().timeout(limit);
      final hs = <String, String>{};
      res.headers.forEach((k, v) {
        if (v.isNotEmpty) hs[k.toLowerCase()] = v.first;
      });
      final r = _Resp(res.statusCode, hs, text);
      if (r.status >= 200 && r.status < 300 || r.status == 304) return r;
      throw ApiException.fromResponse(r.status, await r.json(), headers: hs);
    } on ApiException {
      rethrow;
    } on Object catch (e) {
      try {
        req.abort();
      } on Object {
        // already finished
      }
      _transportError(e, sent: sent, sideEffect: sideEffect);
    }
  }

  Future<Json> _object(_Resp r) async => asJson(await r.json());

  // ------------------------------------------------------------ host

  /// `GET /v1/health` (no token). True when the gateway and daemon are up.
  Future<bool> health() async {
    final r = await _request('GET', ['v1', 'health'], auth: false);
    return jBool((await _object(r))['ok']);
  }

  /// `GET /v1/info`.
  Future<Info> info() async =>
      Info.fromJson(await _object(await _request('GET', ['v1', 'info'])));

  /// `POST /v1/pair`: exchanges the one-time [code] for a device token. The
  /// request is held open until the host approves (up to the 5 minute life of
  /// the code), so [timeout] defaults to 6 minutes. Unauthenticated.
  Future<PairResult> pair({
    required String code,
    required String name,
    String? platform,
    Duration timeout = const Duration(minutes: 6),
  }) async {
    final r = await _request(
      'POST',
      ['v1', 'pair'],
      auth: false,
      sideEffect: true,
      timeout: timeout,
      body: compact({'code': code, 'name': name, 'platform': platform}),
    );
    return PairResult.fromJson(await _object(r));
  }

  // ------------------------------------------------------------ sessions

  /// `GET /v1/sessions`; with [ifNoneMatch] a `304` yields
  /// [SessionList.notModified].
  Future<SessionList> listSessions({String? ifNoneMatch}) async {
    final r = await _request(
      'GET',
      ['v1', 'sessions'],
      headers: {'If-None-Match': ?ifNoneMatch},
    );
    if (r.status == 304) {
      return SessionList(
        sessions: const [],
        etag: r.headers['etag'] ?? ifNoneMatch,
        notModified: true,
      );
    }
    final j = await _object(r);
    return SessionList(
      sessions: jList(j['sessions'], SessionSummary.fromJson),
      etag: r.headers['etag'],
    );
  }

  /// `GET /v1/sessions/{name}`.
  Future<SessionSummary> getSession(String name) async =>
      SessionSummary.fromJson(
        await _object(await _request('GET', ['v1', 'sessions', name])),
      );

  /// `POST /v1/sessions`. A fresh `Idempotency-Key` is generated unless
  /// [idempotencyKey] is given (pass the same one when retrying).
  Future<SessionSummary> runSession(
    RunRequest req, {
    String? idempotencyKey,
  }) async => SessionSummary.fromJson(
    await _object(
      await _request(
        'POST',
        ['v1', 'sessions'],
        body: req.toJson(),
        sideEffect: true,
        headers: {'Idempotency-Key': idempotencyKey ?? _uuid.v4()},
      ),
    ),
  );

  /// `POST /v1/sessions/{name}/kill`.
  Future<SessionSummary> killSession(String name, {String? signal}) async =>
      SessionSummary.fromJson(
        await _object(
          await _request(
            'POST',
            ['v1', 'sessions', name, 'kill'],
            body: signal == null ? null : {'signal': signal},
            sideEffect: true,
          ),
        ),
      );

  /// `DELETE /v1/sessions/{name}` (`force` = kill, then remove).
  Future<void> removeSession(String name, {bool force = false}) async {
    await _request(
      'DELETE',
      ['v1', 'sessions', name],
      query: force ? {'force': 'true'} : null,
      sideEffect: true,
    );
  }

  // ------------------------------------------------------------ view

  /// `GET /v1/sessions/{name}/view`. [ifNoneMatch] is the ETag of the view the
  /// caller holds; a `304` (or an `unchanged` marker) yields [ViewNotModified].
  Future<ViewResult> getView(
    String name, {
    String? ifNoneMatch,
    int? lines,
    int? from,
    bool scrollback = false,
  }) async {
    final r = await _request(
      'GET',
      ['v1', 'sessions', name, 'view'],
      query: {
        if (lines != null) 'lines': '$lines',
        if (from != null) 'from': '$from',
        if (scrollback) 'scrollback': 'true',
      },
      headers: {'If-None-Match': ?ifNoneMatch},
    );
    final etag = r.headers['etag'];
    if (r.status == 304) {
      return ViewNotModified(etag: etag ?? ifNoneMatch);
    }
    final j = await _object(r);
    if (jBool(j['unchanged'])) {
      return ViewNotModified(
        etag: etag,
        revision: jIntOrNull(j['revision']),
        epoch: jStringOrNull(j['epoch']),
      );
    }
    if (jBool(j['patch'])) {
      return ViewPatched(
        base: jInt(j['base']),
        revision: jInt(j['revision']),
        raw: j,
        etag: etag,
      );
    }
    return ViewFull(View.fromJson(j), etag: etag);
  }

  /// `POST /v1/sessions/{name}/view/ack`; returns the revision after the ack.
  Future<int> ackView(String name) async {
    final r = await _request('POST', [
      'v1',
      'sessions',
      name,
      'view',
      'ack',
    ], sideEffect: true);
    return jInt((await _object(r))['revision']);
  }

  /// `POST /v1/sessions/{name}/act`. Always sends an `Idempotency-Key`
  /// (generated unless [idempotencyKey]; reuse it to retry the same action).
  /// [ifMatch] is the view ETag the user acted on (it overrides `revision` and
  /// `epoch` in the body). A stale view answers [ApiException] 409/412 with
  /// [ApiException.freshView]; a lost answer is `outcome_unknown`.
  Future<ActResult> act(
    String name,
    ActRequest req, {
    String? ifMatch,
    String? idempotencyKey,
  }) async {
    final r = await _request(
      'POST',
      ['v1', 'sessions', name, 'act'],
      body: req.toJson(),
      sideEffect: true,
      headers: {
        'Idempotency-Key': idempotencyKey ?? _uuid.v4(),
        'If-Match': ?ifMatch,
      },
    );
    return ActResult.fromJson(
      await _object(r),
      replayed: r.headers['idempotent-replayed'] == 'true',
    );
  }

  // ------------------------------------------------------------ terminal

  /// `GET /v1/sessions/{name}/terminal` (L0 snapshot; `format` text | ansi).
  Future<TerminalSnapshot> getTerminal(
    String name, {
    String format = 'text',
    bool scrollback = false,
    bool ghost = false,
  }) async {
    final r = await _request(
      'GET',
      ['v1', 'sessions', name, 'terminal'],
      query: {
        if (format != 'text') 'format': format,
        if (scrollback) 'scrollback': 'true',
        if (ghost) 'ghost': 'true',
      },
    );
    return TerminalSnapshot.fromJson(await _object(r));
  }

  // ------------------------------------------------------------ mailbox

  /// `GET /v1/mailbox`. [mailbox] peeks at another mailbox (admin scope).
  Future<MessagePage> listMailbox({
    bool all = false,
    String? mailbox,
    int? limit,
    int? before,
  }) async {
    final r = await _request(
      'GET',
      ['v1', 'mailbox'],
      query: {
        if (all) 'all': 'true',
        'mailbox': ?mailbox,
        if (limit != null) 'limit': '$limit',
        if (before != null) 'before': '$before',
      },
    );
    final j = await _object(r);
    return MessagePage(
      messages: jList(j['messages'], Message.fromJson),
      nextBefore: jIntOrNull(j['next_before']),
    );
  }

  /// `GET /v1/mailbox/messages/{id}` (never marks it read).
  Future<Message> getMessage(int id, {String? mailbox}) async =>
      Message.fromJson(
        await _object(
          await _request(
            'GET',
            ['v1', 'mailbox', 'messages', '$id'],
            query: {'mailbox': ?mailbox},
          ),
        ),
      );

  /// `POST /v1/mailbox/messages`.
  Future<Message> sendMessage(
    SendRequest req, {
    String? idempotencyKey,
  }) async => Message.fromJson(
    await _object(
      await _request(
        'POST',
        ['v1', 'mailbox', 'messages'],
        body: req.toJson(),
        sideEffect: true,
        headers: {'Idempotency-Key': idempotencyKey ?? _uuid.v4()},
      ),
    ),
  );

  /// `POST /v1/mailbox/messages/{id}/reply`.
  Future<Message> replyMessage(
    int id,
    String body, {
    String? idempotencyKey,
  }) async => Message.fromJson(
    await _object(
      await _request(
        'POST',
        ['v1', 'mailbox', 'messages', '$id', 'reply'],
        body: {'body': body},
        sideEffect: true,
        headers: {'Idempotency-Key': idempotencyKey ?? _uuid.v4()},
      ),
    ),
  );

  /// `POST /v1/mailbox/messages/{id}/read`.
  Future<Message> markMessageRead(int id) async => Message.fromJson(
    await _object(
      await _request('POST', [
        'v1',
        'mailbox',
        'messages',
        '$id',
        'read',
      ], sideEffect: true),
    ),
  );

  // ------------------------------------------------------------ streams

  /// `GET /v1/events` as a resumable stream. [sessions] / [kinds] filter on
  /// the server. [heartbeat] must match the gateway's `sse_heartbeat`.
  SseClient eventsStream({
    List<String>? sessions,
    List<String>? kinds,
    Duration heartbeat = const Duration(seconds: 15),
  }) => SseClient(
    heartbeat: heartbeat,
    connect: (last) => _openSse(
      ['v1', 'events'],
      {
        if (sessions != null && sessions.isNotEmpty)
          'session': sessions.join(','),
        if (kinds != null && kinds.isNotEmpty) 'kind': kinds.join(','),
      },
      last,
    ),
  );

  /// `GET /v1/sessions/{name}/view/stream` as a resumable stream; feed its
  /// events to a [ViewState]. Cancel it on `bye`.
  SseClient viewStream(
    String name, {
    int? tail,
    Duration heartbeat = const Duration(seconds: 15),
  }) => SseClient(
    heartbeat: heartbeat,
    connect: (last) => _openSse(
      ['v1', 'sessions', name, 'view', 'stream'],
      {if (tail != null) 'tail': '$tail'},
      last,
    ),
  );

  Future<SseConnection> _openSse(
    List<String> segments,
    Map<String, String> query,
    String? lastEventId,
  ) async {
    _rejected = null;
    HttpClientRequest req;
    try {
      req = await _http
          .openUrl('GET', _uri(segments, query))
          .timeout(requestTimeout);
    } on Object catch (e) {
      _transportError(e, sent: false, sideEffect: false);
    }
    try {
      req.followRedirects = false;
      req.headers
        ..set(HttpHeaders.acceptHeader, 'text/event-stream')
        ..set(HttpHeaders.cacheControlHeader, 'no-cache')
        ..set(HttpHeaders.authorizationHeader, 'Bearer ${_token._value}');
      if (lastEventId != null) req.headers.set('Last-Event-ID', lastEventId);
      final res = await req.close().timeout(requestTimeout);
      if (res.statusCode != 200) {
        final text = await res
            .transform(utf8.decoder)
            .join()
            .timeout(requestTimeout);
        final hs = <String, String>{};
        res.headers.forEach((k, v) {
          if (v.isNotEmpty) hs[k.toLowerCase()] = v.first;
        });
        Object? body;
        try {
          body = jsonDecode(text);
        } on FormatException {
          body = null;
        }
        throw ApiException.fromResponse(res.statusCode, body, headers: hs);
      }
      return SseConnection(
        body: res,
        close: () {
          try {
            req.abort();
          } on Object {
            // already closed
          }
        },
      );
    } on ApiException {
      rethrow;
    } on Object catch (e) {
      try {
        req.abort();
      } on Object {
        // ignore
      }
      _transportError(e, sent: true, sideEffect: false);
    }
  }
}
