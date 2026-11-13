import 'dart:async';
import 'dart:convert';
import 'dart:math';

import 'package:agentd_app/api/agentd_api.dart';
import 'package:test/test.dart';

List<SseItem> parseAll(Iterable<String> chunks) {
  final p = SseParser();
  return [for (final c in chunks) ...p.feed(c)];
}

String dump(List<SseItem> items) => items
    .map(
      (i) => switch (i) {
        SseEvent() => 'E(${i.id},${i.event},${i.data})',
        SseComment() => 'C(${i.text})',
        SseRetry() => 'R(${i.milliseconds})',
      },
    )
    .join('|');

class _Fake {
  final conns = <StreamController<List<int>>>[];
  final lastIds = <String?>[];
  final times = <DateTime>[];

  /// Behaviour per attempt (index = attempt number); default: open a stream.
  final failures = <int, Object>{};

  Future<SseConnection> connect(String? last) async {
    final n = lastIds.length;
    lastIds.add(last);
    times.add(DateTime.now());
    final f = failures[n];
    if (f != null) throw f;
    final c = StreamController<List<int>>();
    conns.add(c);
    return SseConnection(body: c.stream, close: () {});
  }

  void send(int i, String text) => conns[i].add(utf8.encode(text));
}

Future<void> pump([int ms = 30]) =>
    Future<void>.delayed(Duration(milliseconds: ms));

void main() {
  group('SseParser', () {
    const stream =
        'retry: 3000\n\n'
        ': ping\n\n'
        'id: 1:5\nevent: status.changed\ndata: {"a":1}\n\n'
        'data: line1\ndata: line2\n\n'
        'event: noid\ndata:nospace\n\n';

    test('whole input', () {
      expect(
        dump(parseAll([stream])),
        'R(3000)|C(ping)|E(1:5,status.changed,{"a":1})'
        '|E(1:5,message,line1\nline2)|E(1:5,noid,nospace)',
      );
    });

    test('every chunking gives the same result (1, 2, 3, 7 chars)', () {
      final whole = dump(parseAll([stream]));
      for (final size in [1, 2, 3, 7, 13]) {
        final chunks = [
          for (var i = 0; i < stream.length; i += size)
            stream.substring(i, min(i + size, stream.length)),
        ];
        expect(dump(parseAll(chunks)), whole, reason: 'chunk size $size');
      }
    });

    test('CRLF and lone CR line ends, including a split CRLF', () {
      final a = dump(parseAll(['id: 7\r\ndata: x\r\n\r\n']));
      expect(a, 'E(7,message,x)');
      expect(dump(parseAll(['data: y\r\r'])), 'E(null,message,y)');
      expect(dump(parseAll(['data: z\r', '\n\r', '\n'])), 'E(null,message,z)');
    });

    test('a block without data is not dispatched, id persists', () {
      final p = SseParser();
      expect(p.feed('id: 9\n\n'), isEmpty);
      expect(p.lastEventId, '9');
      final out = p.feed('data: a\n\n');
      expect((out.single as SseEvent).id, '9');
    });

    test('id with NUL is ignored; bad retry is ignored; empty data line', () {
      final p = SseParser();
      p.feed('id: a\u0000b\n');
      expect(p.lastEventId, isNull);
      expect(p.feed('retry: soon\n'), isEmpty);
      final out = p.feed('data\n\n');
      expect((out.single as SseEvent).data, '');
    });

    test('unknown fields are ignored; unknown event names pass through', () {
      final out = parseAll(['foo: bar\nevent: brand.new\ndata: {}\n\n']);
      expect(dump(out), 'E(null,brand.new,{})');
    });
  });

  test('compareEventIds', () {
    expect(compareEventIds('7:10', '7:9'), greaterThan(0));
    expect(compareEventIds('7:9', '7:9'), 0);
    expect(compareEventIds('7:2', '8:1'), isNull);
    expect(compareEventIds('1760-4121:20', '1760-4121:3'), greaterThan(0));
    expect(compareEventIds('e1-ia-r5', 'e1-ia-r6'), lessThan(0));
    expect(compareEventIds('e1-ia-r5', 'e1-ib-r6'), isNull);
    expect(compareEventIds('junk', 'junk'), isNull);
  });

  group('SseClient', () {
    test(
      'reconnects with Last-Event-ID and drops replayed duplicates',
      () async {
        final f = _Fake();
        final c = SseClient(
          connect: f.connect,
          backoffBase: const Duration(milliseconds: 5),
          backoffMax: const Duration(milliseconds: 10),
        );
        final got = <String>[];
        final sub = c.events().listen((e) => got.add('${e.event}@${e.id}'));
        await pump();
        f.send(
          0,
          'id: 1:1\nevent: a\ndata: {}\n\nid: 1:2\nevent: b\ndata: {}\n\n',
        );
        await pump();
        expect(c.state, SseState.connected);
        await f.conns[0].close(); // server drops the stream
        await pump(80);
        expect(f.lastIds, [null, '1:2']);
        // The gateway replays 1:2 and sends 1:3: only 1:3 is new.
        f.send(
          1,
          'id: 1:2\nevent: b\ndata: {}\n\nid: 1:3\nevent: c\ndata: {}\n\n',
        );
        await pump();
        expect(got, ['a@1:1', 'b@1:2', 'c@1:3']);
        await sub.cancel();
        expect(c.state, SseState.stopped);
      },
    );

    test(
      'hello, resync and full views are never dropped as duplicates',
      () async {
        final f = _Fake();
        final c = SseClient(connect: f.connect);
        final got = <String>[];
        final sub = c.events().listen((e) => got.add(e.event));
        await pump();
        f.send(
          0,
          'id: 1:5\nevent: status.changed\ndata: {}\n\n'
          'event: hello\ndata: {}\n\n'
          'id: 1:5\nevent: resync\ndata: {}\n\n'
          'id: 1:5\nevent: status.changed\ndata: {}\n\n', // duplicate: dropped
        );
        await pump();
        expect(got, ['status.changed', 'hello', 'resync']);
        await sub.cancel();
      },
    );

    test('a different epoch is never treated as a duplicate', () async {
      final f = _Fake();
      final c = SseClient(connect: f.connect);
      final got = <String>[];
      final sub = c.events().listen((e) => got.add(e.id!));
      await pump();
      f.send(0, 'id: 9:50\ndata: {}\n\nid: 10:1\ndata: {}\n\n');
      await pump();
      expect(got, ['9:50', '10:1']);
      await sub.cancel();
    });

    test(
      'silence beyond 2x heartbeat reconnects; pings keep it alive',
      () async {
        final f = _Fake();
        final c = SseClient(
          connect: f.connect,
          heartbeat: const Duration(milliseconds: 40), // liveness 80 ms
          backoffBase: const Duration(milliseconds: 1),
          backoffMax: const Duration(milliseconds: 2),
        );
        final sub = c.events().listen((_) {});
        await pump(10);
        expect(f.conns, hasLength(1));
        // Pings every 30 ms for 200 ms: still the same connection.
        for (var i = 0; i < 7; i++) {
          f.send(0, ': ping\n\n');
          await pump(30);
        }
        expect(f.conns, hasLength(1));
        // Now silence.
        await pump(200);
        expect(f.conns.length, greaterThan(1));
        await sub.cancel();
      },
    );

    test('401 is terminal: error, close, no retry', () async {
      final f = _Fake()
        ..failures[0] = ApiException(
          statusCode: 401,
          code: 'unauthenticated',
          message: 'no',
        );
      final c = SseClient(
        connect: f.connect,
        backoffBase: const Duration(milliseconds: 1),
      );
      final errors = <Object>[];
      final done = Completer<void>();
      c.events().listen((_) {}, onError: errors.add, onDone: done.complete);
      await done.future.timeout(const Duration(seconds: 2));
      expect(errors.single, isA<ApiException>());
      await pump(50);
      expect(f.lastIds, hasLength(1));
      expect(c.state, SseState.stopped);
    });

    test('pin mismatch is terminal', () async {
      final f = _Fake()
        ..failures[0] = PinMismatchException(expected: 'a', seen: 'b');
      final c = SseClient(connect: f.connect);
      final errors = <Object>[];
      final done = Completer<void>();
      c.events().listen((_) {}, onError: errors.add, onDone: done.complete);
      await done.future.timeout(const Duration(seconds: 2));
      expect(errors.single, isA<PinMismatchException>());
    });

    test('503 and network errors are retried with backoff', () async {
      final f = _Fake()
        ..failures[0] = ApiException(
          statusCode: 503,
          code: 'daemon_unavailable',
          message: 'down',
          retryable: true,
        )
        ..failures[1] = ConnectionException('refused')
        ..failures[2] = ApiException(
          statusCode: 429,
          code: 'too_many_streams',
          message: 'x',
        );
      final c = SseClient(
        connect: f.connect,
        backoffBase: const Duration(milliseconds: 5),
        backoffMax: const Duration(milliseconds: 20),
      );
      final sub = c.events().listen((_) {});
      await pump(300);
      expect(f.lastIds.length, 4); // 3 failures, then a connection
      expect(f.conns, hasLength(1));
      await sub.cancel();
    });

    test(
      'backoff grows exponentially up to the cap (full jitter at its max)',
      () async {
        final f = _Fake();
        for (var i = 0; i < 6; i++) {
          f.failures[i] = ConnectionException('down');
        }
        final c = SseClient(
          connect: f.connect,
          backoffBase: const Duration(milliseconds: 20),
          backoffMax: const Duration(milliseconds: 80),
          random: _Max(),
        );
        final sub = c.events().listen((_) {});
        await pump(600);
        await sub.cancel();
        final gaps = [
          for (var i = 1; i < f.times.length; i++)
            f.times[i].difference(f.times[i - 1]).inMilliseconds,
        ];
        // Ceilings: 20, 40, 80, 80, 80 ms (timers never fire early).
        const want = [20, 40, 80, 80, 80];
        for (var i = 0; i < min(gaps.length, want.length); i++) {
          expect(gaps[i], greaterThanOrEqualTo(want[i] - 3), reason: 'gap $i');
        }
        expect(gaps.length, greaterThanOrEqualTo(4));
      },
    );

    test('a retry: hint becomes the backoff base', () async {
      final f = _Fake();
      final c = SseClient(
        connect: f.connect,
        backoffBase: const Duration(milliseconds: 500),
        backoffMax: const Duration(seconds: 5),
        random: _Max(),
      );
      final sub = c.events().listen((_) {});
      await pump();
      f.send(0, 'retry: 15\n\n');
      await pump();
      final sw = Stopwatch()..start();
      await f.conns[0].close();
      while (f.conns.length < 2 && sw.elapsedMilliseconds < 400) {
        await pump(5);
      }
      expect(f.conns, hasLength(2));
      expect(sw.elapsedMilliseconds, lessThan(300));
      await sub.cancel();
    });

    test('multi-byte UTF-8 split across network chunks', () async {
      final f = _Fake();
      final c = SseClient(connect: f.connect);
      final got = <String>[];
      final sub = c.events().listen((e) => got.add(e.data));
      await pump();
      final bytes = utf8.encode('data: café ✨\n\n');
      for (final b in bytes) {
        f.conns[0].add([b]);
      }
      await pump();
      expect(got, ['café ✨']);
      await sub.cancel();
    });
  });
}

class _Max implements Random {
  @override
  bool nextBool() => true;
  @override
  double nextDouble() => 1.0;
  @override
  int nextInt(int max) => max - 1;
}
