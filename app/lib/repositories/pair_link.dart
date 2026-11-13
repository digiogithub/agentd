/// Parsing of `agentd://pair` and `agentd://open` links (security-remote.md
/// section 3.2, frontend-app.md sections 5.3 and 6.3). Pure Dart; every input
/// is untrusted (QR, deep link, clipboard).
library;

import '../api/loopback.dart';

class PairLinkException implements Exception {
  PairLinkException(this.message);
  final String message;
  @override
  String toString() => 'PairLinkException($message)';
}

/// Crockford base32 minus separators: 12 symbols, shown `XXXX-XXXX-XXXX`.
String normalizePairCode(String raw) {
  final s = raw
      .toUpperCase()
      .replaceAll(RegExp(r'[\s-]'), '')
      .replaceAll('O', '0')
      .replaceAll('I', '1')
      .replaceAll('L', '1');
  if (!RegExp(r'^[0-9A-HJKMNP-TV-Z]{12}$').hasMatch(s)) {
    throw PairLinkException('The pairing code must have 12 characters.');
  }
  return '${s.substring(0, 4)}-${s.substring(4, 8)}-${s.substring(8)}';
}

/// Validates a gateway base URL: https, or http for loopback hosts (the
/// Android emulator alias `10.0.2.2` only in debug builds, or with
/// [allowEmulator]).
Uri parseBaseUrl(String raw, {bool? allowEmulator}) {
  final Uri u;
  try {
    u = Uri.parse(raw.trim());
  } on FormatException {
    throw PairLinkException('The host URL is not valid.');
  }
  if (!u.hasAuthority || u.host.isEmpty) {
    throw PairLinkException('The host URL is not valid.');
  }
  if (u.scheme != 'https' && u.scheme != 'http') {
    throw PairLinkException('The host URL must start with https://.');
  }
  if (u.scheme == 'http' &&
      !isLoopbackHost(u.host, allowEmulator: allowEmulator)) {
    throw PairLinkException(
      'Plain http:// is only allowed for localhost. Use https://.',
    );
  }
  if (u.userInfo.isNotEmpty || u.hasQuery || u.hasFragment) {
    throw PairLinkException('The host URL must not carry credentials.');
  }
  if (u.path.isNotEmpty && u.path != '/') {
    throw PairLinkException('The host URL must not have a path.');
  }
  return Uri(scheme: u.scheme, host: u.host, port: u.hasPort ? u.port : null);
}

String? _validPin(String? fp) {
  if (fp == null || fp.isEmpty) return null;
  if (!RegExp(r'^sha256:[0-9a-fA-F]{64}$').hasMatch(fp)) {
    throw PairLinkException('The certificate fingerprint is not valid.');
  }
  return 'sha256:${fp.substring(7).toLowerCase()}';
}

class PairLink {
  const PairLink({
    required this.baseUrl,
    required this.code,
    this.pin,
    this.hostname,
  });

  /// Builds a link from manual form fields. [code] and [fp] are validated.
  factory PairLink.fromFields({
    required String url,
    required String code,
    String? fp,
    String? hostname,
  }) {
    final base = parseBaseUrl(url);
    final pin = _validPin(fp?.trim());
    if (pin != null && base.scheme != 'https') {
      throw PairLinkException('A fingerprint needs an https:// URL.');
    }
    return PairLink(
      baseUrl: base,
      code: normalizePairCode(code),
      pin: pin,
      hostname: hostname,
    );
  }

  /// Parses `agentd://pair?v=1&u=...&c=...&fp=...&h=...`.
  factory PairLink.parse(String raw) {
    final Uri u;
    try {
      u = Uri.parse(raw.trim());
    } on FormatException {
      throw PairLinkException('Not an agentd pairing link.');
    }
    if (u.scheme != 'agentd' || u.host != 'pair') {
      throw PairLinkException('Not an agentd pairing link.');
    }
    final q = u.queryParameters;
    if (q['v'] != '1') {
      throw PairLinkException(
        'Unsupported pairing link version. Update the app or agentd.',
      );
    }
    final url = q['u'];
    final code = q['c'];
    if (url == null || url.isEmpty || code == null || code.isEmpty) {
      throw PairLinkException('The pairing link is incomplete.');
    }
    final h = q['h'];
    return PairLink.fromFields(
      url: url,
      code: code,
      fp: q['fp'],
      hostname: h == null || h.isEmpty ? null : h,
    );
  }

  final Uri baseUrl;

  /// Normalized `XXXX-XXXX-XXXX`.
  final String code;

  /// `sha256:<hex>` or null (system trust).
  final String? pin;
  final String? hostname;

  /// Label shown to the user: the advertised hostname or the URL host.
  String get displayName => hostname ?? baseUrl.host;

  /// Re-encodes as a pair link (used to carry a prefill through the router).
  String toUri() => Uri(
    scheme: 'agentd',
    host: 'pair',
    queryParameters: {
      'v': '1',
      'u': baseUrl.toString(),
      'c': code,
      'fp': ?pin,
      'h': ?hostname,
    },
  ).toString();
}

/// `agentd://open?h=<host>&s=<session>`.
class OpenLink {
  const OpenLink({required this.host, required this.session});

  factory OpenLink.parse(Uri u) {
    if (u.scheme != 'agentd' || u.host != 'open') {
      throw PairLinkException('Not an agentd open link.');
    }
    final h = u.queryParameters['h'];
    final s = u.queryParameters['s'];
    if (h == null || h.isEmpty || s == null || s.isEmpty) {
      throw PairLinkException('The link is incomplete.');
    }
    return OpenLink(host: h, session: s);
  }

  final String host;
  final String session;
}
