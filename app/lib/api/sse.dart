/// Hand-written Server-Sent Events client (frontend-app.md section 4.3).
///
/// [SseParser] turns text chunks into frames; [SseClient] owns the
/// connection lifecycle: `Last-Event-ID` resume, jittered backoff, liveness
/// timeout and de-duplication by event id.
library;

import 'dart:async';
import 'dart:convert';
import 'dart:math';

import 'errors.dart';

// ---------------------------------------------------------------- parser

sealed class SseItem {
  const SseItem();
}

/// One dispatched event. [id] is the last event id in effect (it persists
/// across events, as the SSE spec says); [event] defaults to `message`.
class SseEvent extends SseItem {
  const SseEvent({this.id, required this.event, required this.data});
  final String? id;
  final String event;
  final String data;

  @override
  String toString() => 'SseEvent(id: $id, event: $event, data: $data)';
}

/// A `:` comment line; the gateway sends `: ping` as heartbeat.
class SseComment extends SseItem {
  const SseComment(this.text);
  final String text;
}

/// A `retry:` field (reconnect delay hint, milliseconds).
class SseRetry extends SseItem {
  const SseRetry(this.milliseconds);
  final int milliseconds;
}

/// Incremental parser of the `text/event-stream` format. Feed it decoded text
/// in chunks of any size; lines may end in `\n`, `\r\n` or `\r`.
class SseParser {
  String _buf = '';
  bool _lastWasCr = false;
  String? _id;
  String _event = '';
  final StringBuffer _data = StringBuffer();
  bool _hasData = false;

  /// Last event id seen (`Last-Event-ID` for a reconnect).
  String? get lastEventId => _id;

  List<SseItem> feed(String chunk) {
    final out = <SseItem>[];
    if (chunk.isEmpty) return out;
    var text = chunk;
    // A "\n" right after a chunk-final "\r" belongs to the same line break.
    if (_lastWasCr && text.startsWith('\n')) text = text.substring(1);
    _lastWasCr = false;
    text = _buf + text;
    _buf = '';
    var start = 0;
    var i = 0;
    while (i < text.length) {
      final c = text.codeUnitAt(i);
      if (c == 0x0A || c == 0x0D) {
        _line(text.substring(start, i), out);
        if (c == 0x0D) {
          if (i + 1 < text.length) {
            if (text.codeUnitAt(i + 1) == 0x0A) i++;
          } else {
            _lastWasCr = true;
          }
        }
        start = i + 1;
      }
      i++;
    }
    _buf = text.substring(start);
    return out;
  }

  void _line(String line, List<SseItem> out) {
    if (line.isEmpty) {
      if (_hasData) {
        out.add(
          SseEvent(
            id: _id,
            event: _event.isEmpty ? 'message' : _event,
            data: _data.toString(),
          ),
        );
      }
      _event = '';
      _data.clear();
      _hasData = false;
      return;
    }
    if (line.startsWith(':')) {
      out.add(SseComment(line.substring(1).trimLeft()));
      return;
    }
    final colon = line.indexOf(':');
    final field = colon < 0 ? line : line.substring(0, colon);
    var value = colon < 0 ? '' : line.substring(colon + 1);
    if (value.startsWith(' ')) value = value.substring(1);
    switch (field) {
      case 'event':
        _event = value;
      case 'data':
        if (_hasData) _data.write('\n');
        _data.write(value);
        _hasData = true;
      case 'id':
        if (!value.contains('\u0000')) _id = value;
      case 'retry':
        final ms = int.tryParse(value);
        if (ms != null && ms >= 0) out.add(SseRetry(ms));
    }
  }
}

// ---------------------------------------------------------------- ordering

/// Compares two event ids of the same stream. Returns null when they are not
/// comparable (different epoch / incarnation, or an unknown format), in which
/// case an event must not be dropped.
///
/// Formats: `/v1/events` uses `<epoch>:<seq>`; view streams use
/// `e<epoch>-i<instance>-r<revision>`.
int? compareEventIds(String a, String b) {
  final pa = _parseId(a);
  final pb = _parseId(b);
  if (pa == null || pb == null || pa.$1 != pb.$1) return null;
  return pa.$2.compareTo(pb.$2);
}

final _viewId = RegExp(r'^(e.+-i[^-]+)-r(\d+)$');

(String, int)? _parseId(String id) {
  final m = _viewId.firstMatch(id);
  if (m != null) return (m.group(1)!, int.parse(m.group(2)!));
  final colon = id.lastIndexOf(':');
  if (colon > 0) {
    final seq = int.tryParse(id.substring(colon + 1));
    if (seq != null) return (id.substring(0, colon), seq);
  }
  return null;
}

// ---------------------------------------------------------------- client

/// An open event-stream response.
class SseConnection {
  SseConnection({required this.body, required this.close});
  final Stream<List<int>> body;

  /// Aborts the underlying request.
  final void Function() close;
}

/// Opens one connection; [lastEventId] goes into `Last-Event-ID`. Throws
/// [ApiException] for a non-200 answer and [ConnectionException] /
/// [PinMismatchException] for transport failures.
typedef SseConnector = Future<SseConnection> Function(String? lastEventId);

enum SseState {
  connecting,
  connected,

  /// Waiting before the next attempt (after a drop, silence or error).
  reconnecting,

  /// Stopped by a non-retryable answer (401, 421, 404...) or by cancel.
  stopped,
}

/// Events that are never de-duplicated: control frames and full views carry
/// state, and `resync` carries the current head id rather than a new event.
const _neverDeduped = {'hello', 'resync', 'bye', 'view'};

class SseClient {
  SseClient({
    required this.connect,
    this.heartbeat = const Duration(seconds: 15),
    this.backoffBase = const Duration(milliseconds: 500),
    this.backoffMax = const Duration(seconds: 30),
    this.healthyAfter = const Duration(seconds: 10),
    Random? random,
  }) : _random = random ?? Random();

  final SseConnector connect;

  /// Gateway `sse_heartbeat`; the stream is dead after 2x of silence.
  final Duration heartbeat;
  final Duration backoffBase;
  final Duration backoffMax;

  /// A connection that stayed up this long resets the backoff.
  final Duration healthyAfter;
  final Random _random;

  Duration get livenessTimeout => heartbeat * 2;

  final _states = StreamController<SseState>.broadcast(sync: true);

  /// Connection state changes (for the "reconnecting" bar of the UI).
  Stream<SseState> get states => _states.stream;

  SseState _state = SseState.stopped;
  SseState get state => _state;

  String? _lastEventId;
  String? get lastEventId => _lastEventId;

  /// Starts a new stream of events. Cancel the subscription to stop. A
  /// terminal failure (401, 403, 404, 421, other 4xx) is delivered as an
  /// error ([ApiException]) followed by close; transient ones are retried
  /// silently (see [states]). The stream never ends by itself on `bye`: the
  /// consumer decides (a view stream cancels, `/v1/events` just reconnects).
  Stream<SseEvent> events() {
    late StreamController<SseEvent> ctl;
    var cancelled = false;
    Timer? wait;
    Timer? liveness;
    StreamSubscription<String>? sub;
    SseConnection? conn;
    var attempt = 0;
    int? retryHint;
    String? lastApplied;

    void setState(SseState s) {
      _state = s;
      if (!_states.isClosed) _states.add(s);
    }

    void stop() {
      cancelled = true;
      wait?.cancel();
      liveness?.cancel();
      sub?.cancel();
      conn?.close();
      setState(SseState.stopped);
    }

    late void Function() open;

    void scheduleReconnect({Duration? floor}) {
      if (cancelled) return;
      liveness?.cancel();
      sub?.cancel();
      sub = null;
      conn?.close();
      conn = null;
      setState(SseState.reconnecting);
      final base = retryHint == null
          ? backoffBase
          : Duration(milliseconds: retryHint!);
      final cap = backoffMax;
      final exp = base * pow(2, min(attempt, 16)).toInt();
      final ceiling = exp > cap ? cap : exp;
      attempt++;
      var d = Duration(
        microseconds: (ceiling.inMicroseconds * _random.nextDouble()).round(),
      );
      if (floor != null && d < floor) d = floor;
      wait = Timer(d, open);
    }

    void armLiveness() {
      liveness?.cancel();
      liveness = Timer(livenessTimeout, () => scheduleReconnect());
    }

    void deliver(SseEvent e) {
      final id = e.id;
      if (id != null &&
          lastApplied != null &&
          !_neverDeduped.contains(e.event)) {
        final c = compareEventIds(id, lastApplied!);
        if (c != null && c <= 0) return; // already applied
      }
      if (id != null) lastApplied = id;
      if (!ctl.isClosed) ctl.add(e);
    }

    open = () async {
      if (cancelled) return;
      setState(SseState.connecting);
      final started = DateTime.now();
      final parser = SseParser();
      try {
        final c = await connect(_lastEventId);
        if (cancelled) {
          c.close();
          return;
        }
        conn = c;
        setState(SseState.connected);
        armLiveness();
        sub = c.body
            .transform(utf8.decoder)
            .listen(
              (chunk) {
                armLiveness();
                if (attempt > 0 &&
                    DateTime.now().difference(started) >= healthyAfter) {
                  attempt = 0;
                }
                for (final item in parser.feed(chunk)) {
                  switch (item) {
                    case SseRetry(:final milliseconds):
                      retryHint = milliseconds;
                    case SseComment():
                      break; // heartbeat: liveness already re-armed
                    case SseEvent():
                      if (item.id != null) _lastEventId = item.id;
                      deliver(item);
                  }
                }
              },
              onError: (Object _) {
                if (DateTime.now().difference(started) >= healthyAfter) {
                  attempt = 0;
                }
                scheduleReconnect();
              },
              onDone: () {
                if (DateTime.now().difference(started) >= healthyAfter) {
                  attempt = 0;
                }
                scheduleReconnect();
              },
              cancelOnError: true,
            );
      } on ApiException catch (e) {
        if (_retryable(e)) {
          scheduleReconnect(floor: e.retryAfter);
        } else {
          if (!ctl.isClosed) ctl.addError(e);
          stop();
          if (!ctl.isClosed) unawaited(ctl.close());
        }
      } on PinMismatchException catch (e) {
        if (!ctl.isClosed) ctl.addError(e);
        stop();
        if (!ctl.isClosed) unawaited(ctl.close());
      } on Object {
        scheduleReconnect();
      }
    };

    ctl = StreamController<SseEvent>(
      onListen: () {
        cancelled = false;
        open();
      },
      onCancel: stop,
    );
    return ctl.stream;
  }

  static bool _retryable(ApiException e) {
    final s = e.statusCode;
    if (s == 408 || s == 429 || s >= 500) return true;
    return e.retryable;
  }
}
