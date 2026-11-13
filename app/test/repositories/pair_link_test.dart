import 'package:agentd_app/repositories/pair_link.dart';
import 'package:flutter_test/flutter_test.dart';

String link({
  String v = '1',
  String u = 'https://box.ts.net:7477',
  String c = 'ABCD-EFGH-JKMN',
  String? fp,
  String? h = 'box',
}) {
  final q = {'v': v, 'u': u, 'c': c, 'fp': ?fp, 'h': ?h};
  return Uri(scheme: 'agentd', host: 'pair', queryParameters: q).toString();
}

final pin = 'sha256:${'ab' * 32}';

void main() {
  group('PairLink.parse', () {
    test('valid with pin and hostname', () {
      final l = PairLink.parse(link(fp: pin));
      expect(l.baseUrl.toString(), 'https://box.ts.net:7477');
      expect(l.code, 'ABCD-EFGH-JKMN');
      expect(l.pin, pin);
      expect(l.hostname, 'box');
      expect(l.displayName, 'box');
    });

    test('fp and h are optional', () {
      final l = PairLink.parse(link(h: null));
      expect(l.pin, isNull);
      expect(l.displayName, 'box.ts.net');
    });

    test('code is normalized (case, spaces, dashes, look-alikes)', () {
      expect(normalizePairCode('abcd efgh jkmn'), 'ABCD-EFGH-JKMN');
      expect(normalizePairCode('abcdefghjkmn'), 'ABCD-EFGH-JKMN');
      expect(normalizePairCode('O1lI-0000-0000'), '0111-0000-0000');
    });

    test('pin hex is lowercased', () {
      final l = PairLink.parse(link(fp: 'sha256:${'AB' * 32}'));
      expect(l.pin, pin);
    });

    test('round trips through toUri', () {
      final l = PairLink.parse(link(fp: pin));
      final l2 = PairLink.parse(l.toUri());
      expect(l2.baseUrl, l.baseUrl);
      expect(l2.code, l.code);
      expect(l2.pin, l.pin);
      expect(l2.hostname, l.hostname);
    });

    test('http allowed for loopback only', () {
      expect(
        PairLink.parse(link(u: 'http://127.0.0.1:7477')).baseUrl.scheme,
        'http',
      );
      PairLink.parse(link(u: 'http://localhost:7477'));
      PairLink.parse(link(u: 'http://10.0.2.2:7477'));
      expect(
        () => PairLink.parse(link(u: 'http://box.ts.net:7477')),
        throwsA(isA<PairLinkException>()),
      );
      expect(
        () => PairLink.parse(link(u: 'http://192.168.1.5:7477')),
        throwsA(isA<PairLinkException>()),
      );
    });

    final bad = <String, String>{
      'unknown version': link(v: '2'),
      'missing version': 'agentd://pair?u=https://a.b&c=ABCD-EFGH-JKMN',
      'wrong scheme': link().replaceFirst('agentd', 'https'),
      'wrong host': link().replaceFirst('//pair', '//open'),
      'missing url': 'agentd://pair?v=1&c=ABCD-EFGH-JKMN',
      'missing code': 'agentd://pair?v=1&u=https%3A%2F%2Fa.b',
      'short code': link(c: 'ABCD-EFGH'),
      'bad code chars': link(c: 'ABCD-EFGH-JKM!'),
      'ftp url': link(u: 'ftp://box'),
      'no host': link(u: 'https://'),
      'credentials': link(u: 'https://user:pw@box.ts.net'),
      'path': link(u: 'https://box.ts.net/x'),
      'query in url': link(u: 'https://box.ts.net/?a=1'),
      'short pin': link(fp: 'sha256:abcd'),
      'pin without prefix': link(fp: 'ab' * 32),
      'pin with http': link(u: 'http://localhost:1', fp: pin),
      'garbage': 'not a url at all',
      'empty': '',
    };
    bad.forEach((name, raw) {
      test('rejects $name', () {
        expect(() => PairLink.parse(raw), throwsA(isA<PairLinkException>()));
      });
    });

    test('surrounding whitespace is tolerated', () {
      PairLink.parse('  ${link()}\n');
    });
  });

  group('OpenLink', () {
    test('parses h and s', () {
      final o = OpenLink.parse(Uri.parse('agentd://open?h=box&s=codex%40web'));
      expect(o.host, 'box');
      expect(o.session, 'codex@web');
    });
    test('rejects incomplete', () {
      expect(
        () => OpenLink.parse(Uri.parse('agentd://open?h=box')),
        throwsA(isA<PairLinkException>()),
      );
    });
  });
}
