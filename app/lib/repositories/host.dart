/// Paired host record (frontend-app.md section 5.2). Never holds the token.
library;

import 'package:flutter/foundation.dart';

@immutable
class Host {
  const Host({
    required this.id,
    required this.label,
    required this.baseUrl,
    required this.deviceId,
    required this.scope,
    required this.hostname,
    required this.addedAt,
    this.pin,
    this.lastOk,
  });

  factory Host.fromJson(Map<String, Object?> j) => Host(
    id: j['id']! as String,
    label: j['label']! as String,
    baseUrl: Uri.parse(j['base_url']! as String),
    deviceId: (j['device_id'] as String?) ?? '',
    scope: (j['scope'] as String?) ?? '',
    pin: j['tls_pin'] as String?,
    hostname: (j['hostname'] as String?) ?? '',
    addedAt: DateTime.parse(j['added_at']! as String),
    lastOk: j['last_ok'] == null
        ? null
        : DateTime.tryParse(j['last_ok']! as String),
  );

  final String id;
  final String label;
  final Uri baseUrl;
  final String deviceId;

  /// `read`, `write`, `admin` (raw wire string).
  final String scope;

  /// `sha256:<hex>` SPKI pin; null means system trust.
  final String? pin;
  final String hostname;
  final DateTime addedAt;
  final DateTime? lastOk;

  Host copyWith({String? label, DateTime? lastOk}) => Host(
    id: id,
    label: label ?? this.label,
    baseUrl: baseUrl,
    deviceId: deviceId,
    scope: scope,
    hostname: hostname,
    addedAt: addedAt,
    pin: pin,
    lastOk: lastOk ?? this.lastOk,
  );

  Map<String, Object?> toJson() => {
    'id': id,
    'label': label,
    'base_url': baseUrl.toString(),
    'device_id': deviceId,
    'scope': scope,
    if (pin != null) 'tls_pin': pin,
    'hostname': hostname,
    'added_at': addedAt.toUtc().toIso8601String(),
    if (lastOk != null) 'last_ok': lastOk!.toUtc().toIso8601String(),
  };
}
