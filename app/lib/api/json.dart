/// Tolerant JSON accessors shared by the models: a missing or mistyped field
/// yields a default instead of throwing, unknown fields are ignored and unknown
/// enum values map to the enum's `unknown` member (frontend-app.md section 4.5).
library;

typedef Json = Map<String, dynamic>;

/// Returns [v] as a JSON object, or an empty one.
Json asJson(Object? v) {
  if (v is Map) {
    return v.map((k, val) => MapEntry(k.toString(), val));
  }
  return <String, dynamic>{};
}

/// Returns [v] as a JSON object, or null when it is not one.
Json? asJsonOrNull(Object? v) => v is Map ? asJson(v) : null;

String jString(Object? v, [String def = '']) => v is String ? v : def;

String? jStringOrNull(Object? v) => v is String ? v : null;

int jInt(Object? v, [int def = 0]) => v is num ? v.toInt() : def;

int? jIntOrNull(Object? v) => v is num ? v.toInt() : null;

double jDouble(Object? v, [double def = 0]) => v is num ? v.toDouble() : def;

bool jBool(Object? v, [bool def = false]) => v is bool ? v : def;

bool? jBoolOrNull(Object? v) => v is bool ? v : null;

DateTime? jTime(Object? v) => v is String ? DateTime.tryParse(v) : null;

List<T> jList<T>(Object? v, T Function(Json) f) {
  if (v is! List) return <T>[];
  return [
    for (final e in v)
      if (e is Map) f(asJson(e)),
  ];
}

List<String> jStrings(Object? v) {
  if (v is! List) return <String>[];
  return [
    for (final e in v)
      if (e is String) e,
  ];
}

/// Maps [raw] onto the member of [values] whose wire string equals it, or
/// [unknown].
T parseEnum<T>(
  List<T> values,
  Object? raw,
  T unknown,
  String Function(T) wire,
) {
  if (raw is! String) return unknown;
  for (final v in values) {
    if (v != unknown && wire(v) == raw) return v;
  }
  return unknown;
}

/// Removes null entries so `toJson` only carries present fields.
Json compact(Json m) {
  m.removeWhere((_, v) => v == null);
  return m;
}
