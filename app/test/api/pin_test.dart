import 'dart:typed_data';
import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:agentd_app/api/agentd_api.dart';
import 'package:test/test.dart';

String fixture(String n) => File('test/fixtures/$n').readAsStringSync();

Uint8List pemToDer(String pem) {
  final b64 = pem
      .split('\n')
      .where((l) => l.isNotEmpty && !l.startsWith('-----'))
      .join();
  return base64.decode(b64);
}

Future<HttpServer> tlsServer(String name) async {
  final ctx = SecurityContext()
    ..useCertificateChain('test/fixtures/$name.cert.pem')
    ..usePrivateKey('test/fixtures/$name.key.pem');
  final s = await HttpServer.bindSecure(InternetAddress.loopbackIPv4, 0, ctx);
  s.listen((req) {
    req.response.headers.contentType = ContentType.json;
    req.response.write(jsonEncode({'ok': true}));
    req.response.close();
  });
  return s;
}

void main() {
  test('SPKI extraction matches the openssl-computed pin', () {
    for (final n in ['a', 'b']) {
      final want = fixture('$n.pin').trim();
      final got = SpkiPin.ofCertificate(pemToDer(fixture('$n.cert.pem')));
      expect(got.text, want);
      expect(SpkiPin.parse(want).matches(got), isTrue);
    }
    expect(
      SpkiPin.parse(fixture('a.pin').trim())
          .matches(SpkiPin.parse(fixture('b.pin').trim())),
      isFalse,
    );
  });

  test('pin parsing', () {
    expect(() => SpkiPin.parse('md5:abcd'), throwsFormatException);
    expect(() => SpkiPin.parse('sha256:zz'), throwsFormatException);
    expect(SpkiPin.parse('SHA256:${'AB' * 32}').text, 'sha256:${'ab' * 32}');
    expect(() => spkiOf(Uint8List.fromList([1, 2, 3])), throwsFormatException);
  });

  test('matching pin: connects to a self-signed server', () async {
    final s = await tlsServer('a');
    addTearDown(() => s.close(force: true));
    final c = AgentdClient(
      baseUrl: Uri.parse('https://127.0.0.1:${s.port}'),
      token: 't',
      pin: SpkiPin.parse(fixture('a.pin').trim()),
    );
    addTearDown(c.close);
    expect(await c.health(), isTrue);
  });

  test('rotated key: PinMismatchException with expected and seen', () async {
    final s = await tlsServer('b');
    addTearDown(() => s.close(force: true));
    final c = AgentdClient(
      baseUrl: Uri.parse('https://127.0.0.1:${s.port}'),
      token: 't',
      pin: SpkiPin.parse(fixture('a.pin').trim()),
    );
    addTearDown(c.close);
    await expectLater(
      c.health(),
      throwsA(
        isA<PinMismatchException>()
            .having((e) => e.expected, 'expected', fixture('a.pin').trim())
            .having((e) => e.seen, 'seen', fixture('b.pin').trim()),
      ),
    );
  });

  test(
    'without a pin a self-signed certificate is rejected (never trusted)',
    () async {
      final s = await tlsServer('a');
      addTearDown(() => s.close(force: true));
      final c = AgentdClient(
        baseUrl: Uri.parse('https://127.0.0.1:${s.port}'),
        token: 't',
      );
      addTearDown(c.close);
      await expectLater(c.health(), throwsA(isA<ConnectionException>()));
    },
  );

  test('mismatch also stops SSE streams (terminal error)', () async {
    final s = await tlsServer('b');
    addTearDown(() => s.close(force: true));
    final c = AgentdClient(
      baseUrl: Uri.parse('https://127.0.0.1:${s.port}'),
      token: 't',
      pin: SpkiPin.parse(fixture('a.pin').trim()),
    );
    addTearDown(c.close);
    final errs = <Object>[];
    final done = Completer<void>();
    c.eventsStream().events().listen(
      (_) {},
      onError: errs.add,
      onDone: done.complete,
    );
    await done.future.timeout(const Duration(seconds: 5));
    expect(errs.single, isA<PinMismatchException>());
  });
}
