/// Typed errors of the API layer (http-api.md section 2.2).
library;

import 'json.dart';
import 'models.dart';

/// A non-2xx answer carrying the gateway's error envelope
/// `{"error": {code, message, retryable, details}}`, or a client-side failure
/// that is reported with the same shape (see [ApiException.outcomeUnknown]).
class ApiException implements Exception {
  ApiException({
    required this.statusCode,
    required this.code,
    required this.message,
    this.retryable = false,
    this.details = const {},
    this.retryAfter,
    this.etag,
    this.local = false,
  });

  /// Builds an exception from an HTTP answer. [body] may be anything: a body
  /// that is not an envelope yields `http_<status>`.
  factory ApiException.fromResponse(
    int status,
    Object? body, {
    Map<String, String> headers = const {},
  }) {
    final err = body is Map ? asJsonOrNull(body['error']) : null;
    final ra = int.tryParse(headers['retry-after'] ?? '');
    return ApiException(
      statusCode: status,
      code: err == null ? 'http_$status' : jString(err['code'], 'http_$status'),
      message: err == null ? 'HTTP $status' : jString(err['message']),
      retryable: err == null ? false : jBool(err['retryable']),
      details: err == null ? const {} : asJson(err['details']),
      retryAfter: ra == null ? null : Duration(seconds: ra),
      etag: headers['etag'],
    );
  }

  /// The request may have reached the gateway but its answer was lost (the
  /// connection dropped or timed out after the request was sent). Never retry
  /// blindly: look at the view first (http-api.md section 3.6).
  factory ApiException.outcomeUnknown(Object cause) => ApiException(
    statusCode: 0,
    code: 'outcome_unknown',
    message: 'request sent but no answer received: $cause',
    local: true,
  );

  final int statusCode;
  final String code;
  final String message;
  final bool retryable;
  final Json details;
  final Duration? retryAfter;

  /// `ETag` of the answer (the fresh view's tag on an `act` conflict).
  final String? etag;

  /// True when raised by the client itself (no gateway answer behind it).
  final bool local;

  bool get isOutcomeUnknown => code == 'outcome_unknown';
  bool get isUnauthenticated => statusCode == 401;
  bool get isForbidden => statusCode == 403;
  bool get isNotFound => statusCode == 404;

  /// 409 (`stale_revision`, `option_mismatch`, `human_draft`, `exited`,
  /// `starting`, `in_progress`, `outcome_unknown` replay...).
  bool get isConflict => statusCode == 409;

  /// 412: `If-Match` did not match the current view.
  bool get isPreconditionFailed => statusCode == 412;

  /// The conflicting state the gateway attaches to stale `act` answers.
  View? get freshView {
    final v = details['view'];
    return v is Map ? View.fromJson(asJson(v)) : null;
  }

  @override
  String toString() => 'ApiException($statusCode $code: $message)';
}

/// The server certificate's SPKI hash differs from the configured pin.
/// There is no "accept anyway" (frontend-app.md section 4.2).
class PinMismatchException implements Exception {
  PinMismatchException({required this.expected, required this.seen});
  final String expected;
  final String seen;
  @override
  String toString() => 'PinMismatchException(expected $expected, seen $seen)';
}

/// Transport failure before any request was sent (DNS, refused, TLS without a
/// pin mismatch, timeout while connecting). Safe to retry.
class ConnectionException implements Exception {
  ConnectionException(this.message, [this.cause]);
  final String message;
  final Object? cause;
  @override
  String toString() => 'ConnectionException($message)';
}
