/// Maps `agentd://` links to router locations.
library;

import '../repositories/host.dart';
import '../repositories/pair_link.dart';

sealed class DeepLinkTarget {
  const DeepLinkTarget();
}

/// Navigate to [location]. [pairLink] (a raw `agentd://pair` string, it
/// carries the one-time code) travels apart from the location, in memory
/// only ([pendingPairLinkProvider]): never in a route query string.
class DeepLinkRoute extends DeepLinkTarget {
  const DeepLinkRoute(this.location, {this.pairLink});
  final String location;
  final String? pairLink;
}

/// Show [message] and stay.
class DeepLinkIgnored extends DeepLinkTarget {
  const DeepLinkIgnored(this.message);
  final String message;
}

/// `agentd://open` named a host label / hostname shared by several paired
/// hosts: the user picks which one (never guessed).
class DeepLinkChoose extends DeepLinkTarget {
  const DeepLinkChoose(this.hosts, this.session);
  final List<Host> hosts;
  final String session;
}

/// Hosts a link reference may mean. An exact host id always wins (and is
/// unique); otherwise every host whose label matches (case-insensitive); only
/// when no label matches, every host whose hostname does. More than one entry
/// means the reference is ambiguous.
List<Host> resolveHosts(List<Host> hosts, String ref) {
  for (final h in hosts) {
    if (h.id == ref) return [h];
  }
  final r = ref.toLowerCase();
  final byLabel = [
    for (final h in hosts)
      if (h.label.toLowerCase() == r) h,
  ];
  if (byLabel.isNotEmpty) return byLabel;
  return [
    for (final h in hosts)
      if (h.hostname.toLowerCase() == r) h,
  ];
}

/// The single host a reference resolves to, or null (none or ambiguous).
Host? resolveHost(List<Host> hosts, String ref) {
  final m = resolveHosts(hosts, ref);
  return m.length == 1 ? m.first : null;
}

/// Ignores the same link delivered twice in a short time (app_links reports
/// the launching link both as the initial link and on the stream).
class DeepLinkDeduper {
  DeepLinkDeduper({
    this.window = const Duration(seconds: 2),
    DateTime Function()? now,
  }) : _now = now ?? DateTime.now;
  final Duration window;
  final DateTime Function() _now;
  String? _last;
  DateTime? _at;

  /// True when [uri] should be handled (it is not a repeat).
  bool accept(Uri uri) {
    final now = _now();
    final key = uri.toString();
    if (_last == key && _at != null && now.difference(_at!) < window) {
      return false;
    }
    _last = key;
    _at = now;
    return true;
  }
}

String pairLocation({String? rehost}) => Uri(
  path: '/pair',
  queryParameters: rehost == null ? null : {'rehost': rehost},
).toString();

String sessionLocation(Host host, String session) =>
    Uri(pathSegments: ['', 'hosts', host.id, 'sessions', session]).toString();

DeepLinkTarget routeForDeepLink(Uri uri, List<Host> hosts) {
  if (uri.scheme != 'agentd') {
    return const DeepLinkIgnored('Unsupported link.');
  }
  try {
    switch (uri.host) {
      case 'pair':
        final link = PairLink.parse(uri.toString());
        return DeepLinkRoute(pairLocation(), pairLink: link.toUri());
      case 'open':
        final o = OpenLink.parse(uri);
        final m = resolveHosts(hosts, o.host);
        if (m.isEmpty) {
          return DeepLinkIgnored('Unknown host "${o.host}".');
        }
        if (m.length > 1) return DeepLinkChoose(m, o.session);
        return DeepLinkRoute(sessionLocation(m.first, o.session));
    }
  } on PairLinkException catch (e) {
    return DeepLinkIgnored(e.message);
  }
  return const DeepLinkIgnored('Unsupported link.');
}
