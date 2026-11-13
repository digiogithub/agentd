/// SPKI pinning helpers (frontend-app.md section 4.2).
library;

import 'dart:typed_data';

import 'package:crypto/crypto.dart';

/// `sha256:<hex>` of a certificate's SubjectPublicKeyInfo, the form the
/// gateway prints (`agentd serve tls show`) and the pairing QR carries (`fp`).
class SpkiPin {
  SpkiPin._(this.digest);

  /// Parses `sha256:<64 hex>` (case-insensitive hex). Throws [FormatException].
  factory SpkiPin.parse(String s) {
    const prefix = 'sha256:';
    if (!s.toLowerCase().startsWith(prefix)) {
      throw FormatException('pin must start with "$prefix"', s);
    }
    final hex = s.substring(prefix.length);
    if (!RegExp(r'^[0-9a-fA-F]{64}$').hasMatch(hex)) {
      throw FormatException('pin must be 32 bytes of hex', s);
    }
    final out = Uint8List(32);
    for (var i = 0; i < 32; i++) {
      out[i] = int.parse(hex.substring(i * 2, i * 2 + 2), radix: 16);
    }
    return SpkiPin._(out);
  }

  /// Pin of the certificate [der] (a DER X.509 leaf).
  factory SpkiPin.ofCertificate(Uint8List der) =>
      SpkiPin._(Uint8List.fromList(sha256.convert(spkiOf(der)).bytes));

  final Uint8List digest;

  /// Constant-time comparison.
  bool matches(SpkiPin other) {
    var d = 0;
    for (var i = 0; i < digest.length; i++) {
      d |= digest[i] ^ other.digest[i];
    }
    return d == 0;
  }

  String get text =>
      'sha256:${digest.map((b) => b.toRadixString(16).padLeft(2, '0')).join()}';

  @override
  String toString() => text;
}

/// Extracts the DER SubjectPublicKeyInfo (header included) from an X.509
/// certificate. Throws [FormatException] on malformed input.
Uint8List spkiOf(Uint8List der) {
  final cert = _tlv(der, 0);
  if (cert.tag != 0x30) {
    throw const FormatException('certificate not a SEQUENCE');
  }
  final tbs = _tlv(der, cert.contentStart);
  if (tbs.tag != 0x30) {
    throw const FormatException('tbsCertificate not a SEQUENCE');
  }
  var pos = tbs.contentStart;
  // Optional [0] EXPLICIT version, then serial, signature, issuer, validity,
  // subject, subjectPublicKeyInfo.
  var t = _tlv(der, pos);
  if (t.tag == 0xA0) {
    pos = t.end;
  }
  for (var i = 0; i < 5; i++) {
    t = _tlv(der, pos);
    pos = t.end;
  }
  final spki = _tlv(der, pos);
  if (spki.tag != 0x30) throw const FormatException('SPKI not a SEQUENCE');
  return Uint8List.sublistView(der, pos, spki.end);
}

class _Tlv {
  _Tlv(this.tag, this.contentStart, this.end);
  final int tag;
  final int contentStart;
  final int end;
}

_Tlv _tlv(Uint8List b, int pos) {
  if (pos + 2 > b.length) throw const FormatException('truncated DER');
  final tag = b[pos];
  var len = b[pos + 1];
  var start = pos + 2;
  if (len & 0x80 != 0) {
    final n = len & 0x7f;
    if (n == 0 || n > 4 || start + n > b.length) {
      throw const FormatException('bad DER length');
    }
    len = 0;
    for (var i = 0; i < n; i++) {
      len = (len << 8) | b[start + i];
    }
    start += n;
  }
  final end = start + len;
  if (end > b.length) throw const FormatException('DER length past end');
  return _Tlv(tag, start, end);
}
