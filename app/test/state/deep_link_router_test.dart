import 'package:agentd_app/state/deep_link_router.dart';
import 'package:flutter_test/flutter_test.dart';

import '../support/fakes.dart';

void main() {
  final hosts = [
    sampleHost(),
    sampleHost(id: 'h2', label: 'Work Box', hostname: 'work.local'),
  ];
  String? loc(String u) => switch (routeForDeepLink(Uri.parse(u), hosts)) {
    DeepLinkRoute(:final location) => location,
    _ => null,
  };

  test('pair link goes to /pair, the link travels outside the route', () {
    final t = routeForDeepLink(
      Uri.parse(
        'agentd://pair?v=1&u=https%3A%2F%2Fa.b%3A1&c=ABCD-EFGH-JKMN&h=a',
      ),
      hosts,
    ) as DeepLinkRoute;
    expect(t.location, '/pair');
    expect(t.location, isNot(contains('ABCD')));
    expect(Uri.parse(t.location).queryParameters, isEmpty);
    expect(t.pairLink, startsWith('agentd://pair?'));
    expect(t.pairLink, contains('ABCD'));
  });

  test('open resolves by label, hostname, id (case-insensitive)', () {
    expect(loc('agentd://open?h=box&s=codex'), '/hosts/h1/sessions/codex');
    expect(loc('agentd://open?h=WORK%20BOX&s=a'), '/hosts/h2/sessions/a');
    expect(loc('agentd://open?h=work.local&s=a'), '/hosts/h2/sessions/a');
    expect(loc('agentd://open?h=h2&s=a'), '/hosts/h2/sessions/a');
  });

  test('session names are path-encoded', () {
    expect(
      loc('agentd://open?h=box&s=codex%40web'),
      '/hosts/h1/sessions/codex@web',
    );
  });

  test('unknown host, bad pair, other scheme and host are ignored', () {
    for (final u in [
      'agentd://open?h=nope&s=a',
      'agentd://open?h=box',
      'agentd://pair?v=9&u=x&c=y',
      'agentd://other',
      'https://example.com',
    ]) {
      expect(routeForDeepLink(Uri.parse(u), hosts), isA<DeepLinkIgnored>());
    }
  });

  group('ambiguous hosts', () {
    final dup = [
      sampleHost(id: 'a', label: 'Box', hostname: 'one.local'),
      sampleHost(id: 'b', label: 'box', hostname: 'two.local'),
      sampleHost(id: 'c', label: 'Other', hostname: 'one.local'),
    ];

    test('host id wins over any label or hostname', () {
      final r = routeForDeepLink(Uri.parse('agentd://open?h=a&s=x'), dup);
      expect((r as DeepLinkRoute).location, '/hosts/a/sessions/x');
    });

    test('a label shared by several hosts asks the user', () {
      final r = routeForDeepLink(Uri.parse('agentd://open?h=BOX&s=x'), dup);
      expect(r, isA<DeepLinkChoose>());
      expect((r as DeepLinkChoose).hosts.map((h) => h.id), ['a', 'b']);
      expect(r.session, 'x');
    });

    test('a shared hostname asks too; a unique one does not', () {
      expect(
        routeForDeepLink(Uri.parse('agentd://open?h=one.local&s=x'), dup),
        isA<DeepLinkChoose>(),
      );
      expect(
        routeForDeepLink(Uri.parse('agentd://open?h=two.local&s=x'), dup),
        isA<DeepLinkRoute>(),
      );
      expect(resolveHost(dup, 'box'), isNull);
    });
  });

  test('the same link within the window is handled once', () {
    var now = DateTime(2026, 1, 1);
    final d = DeepLinkDeduper(now: () => now);
    final u = Uri.parse('agentd://open?h=box&s=a');
    expect(d.accept(u), isTrue);
    now = now.add(const Duration(milliseconds: 900));
    expect(d.accept(u), isFalse);
    expect(d.accept(Uri.parse('agentd://open?h=box&s=b')), isTrue);
    expect(d.accept(u), isTrue); // not the last one any more
    now = now.add(const Duration(seconds: 3));
    expect(d.accept(u), isTrue);
  });
}
