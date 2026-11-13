/// Pairing state machine (frontend-app.md section 5.3).
library;

import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:uuid/uuid.dart';

import '../api/agentd_api.dart';
import '../platform/device_info.dart';
import '../platform/secret_store.dart';
import '../repositories/host.dart';
import '../repositories/pair_link.dart';
import '../repositories/pairing_gateway.dart';
import 'providers.dart';

enum PairPhase { idle, waiting, success, failed, cancelled }

enum PairFailureKind {
  denied,
  expired,
  invalid,
  rateLimited,
  pinMismatch,
  endpointChanged,
  network,
  secretStore,
  unknown,
}

class PairFailure {
  const PairFailure(this.kind, this.message);
  final PairFailureKind kind;
  final String message;
}

class PairingState {
  const PairingState({
    this.phase = PairPhase.idle,
    this.link,
    this.failure,
    this.host,
    this.warning,
  });

  final PairPhase phase;
  final PairLink? link;
  final PairFailure? failure;

  /// The saved host after [PairPhase.success].
  final Host? host;

  /// Non-fatal note (e.g. health/info failed after pairing).
  final String? warning;

  bool get busy => phase == PairPhase.waiting;
}

/// Friendly message for any failure of the pairing request.
PairFailure mapPairError(Object e, {PairLink? link}) {
  if (e is PinMismatchException) {
    return const PairFailure(
      PairFailureKind.pinMismatch,
      'The certificate of this host does not match the fingerprint in the '
      'pairing link. Compare it with "agentd serve tls show" and scan a fresh '
      'QR code.',
    );
  }
  if (e is SecretStoreUnavailable) {
    return PairFailure(PairFailureKind.secretStore, e.message);
  }
  if (e is ApiException) {
    switch (e.code) {
      case 'pairing_denied':
        return const PairFailure(
          PairFailureKind.denied,
          'The host refused this device.',
        );
      case 'pairing_expired':
        return const PairFailure(
          PairFailureKind.expired,
          'The pairing code expired. Run "agentd serve pair" again.',
        );
      case 'pairing_invalid':
        return const PairFailure(
          PairFailureKind.invalid,
          'The pairing code is wrong, already used or expired. Run '
          '"agentd serve pair" for a new one.',
        );
    }
    if (e.statusCode == 429) {
      final s = e.retryAfter?.inSeconds;
      return PairFailure(
        PairFailureKind.rateLimited,
        s == null
            ? 'Too many attempts. Wait a few minutes and try again.'
            : 'Too many attempts. Try again in $s seconds.',
      );
    }
    if (e.isOutcomeUnknown) {
      return const PairFailure(
        PairFailureKind.network,
        'The connection dropped while waiting for approval. The code may '
        'have been used: run "agentd serve pair" for a new one.',
      );
    }
    return PairFailure(
      PairFailureKind.unknown,
      e.message.isEmpty ? 'The host answered ${e.statusCode}.' : e.message,
    );
  }
  if (e is ConnectionException) {
    return PairFailure(
      PairFailureKind.network,
      'Cannot reach ${link?.baseUrl.host ?? 'the host'}. Check the URL, the '
      'network (Tailscale?) and that "agentd serve" is running.',
    );
  }
  return PairFailure(PairFailureKind.unknown, 'Pairing failed: $e');
}

/// True when [link] points somewhere else than the paired [host] (another
/// URL, another pin, or no pin where there was one).
bool endpointChanged(Host host, PairLink link) =>
    host.baseUrl != link.baseUrl || host.pin != link.pin;

final pairingControllerProvider =
    NotifierProvider.autoDispose<PairingController, PairingState>(
      PairingController.new,
    );

class PairingController extends Notifier<PairingState> {
  int _gen = 0;
  PairingGateway? _pending;

  @override
  PairingState build() {
    ref.onDispose(() {
      _gen++;
      _pending?.close();
    });
    return const PairingState();
  }

  /// Pairs with [link]. With [replaceHostId] the existing host keeps its id,
  /// label and position and only gets the new token/device (re-pair). A
  /// re-pair keeps the host's base URL and certificate pin: when [link]
  /// carries another URL or pin (or drops the pin) nothing is sent unless the
  /// user explicitly confirmed it ([allowEndpointChange], after a warning
  /// dialog). A pin is never dropped silently.
  Future<void> start(
    PairLink link, {
    String? deviceName,
    String? replaceHostId,
    bool allowEndpointChange = false,
  }) async {
    if (state.busy) return;
    if (replaceHostId != null && !allowEndpointChange) {
      final old = (ref.read(hostsProvider).value ?? const <Host>[])
          .where((h) => h.id == replaceHostId)
          .firstOrNull;
      if (old != null && endpointChanged(old, link)) {
        state = PairingState(
          phase: PairPhase.failed,
          link: link,
          failure: const PairFailure(
            PairFailureKind.endpointChanged,
            'This host is already paired with another URL or certificate '
            'fingerprint. Nothing was sent: confirm the change to re-pair '
            'with the new ones.',
          ),
        );
        return;
      }
    }
    final gen = ++_gen;
    final factory = ref.read(gatewayFactoryProvider);
    state = PairingState(phase: PairPhase.waiting, link: link);
    final gw = factory(
      link.baseUrl,
      link.pin == null ? null : SpkiPin.parse(link.pin!),
      '',
    );
    _pending = gw;
    final PairResult res;
    try {
      res = await gw.pair(
        code: link.code,
        name: deviceName ?? defaultDeviceName(),
        platform: platformName(),
      );
    } on Object catch (e) {
      if (gen != _gen) return;
      _pending = null;
      gw.close();
      state = PairingState(
        phase: PairPhase.failed,
        link: link,
        failure: mapPairError(e, link: link),
      );
      return;
    }
    if (gen != _gen) {
      gw.close();
      return;
    }
    _pending = null;
    gw.close();
    await _finish(gen, link, res, replaceHostId);
  }

  Future<void> _finish(
    int gen,
    PairLink link,
    PairResult res,
    String? replaceHostId,
  ) async {
    final pin = link.pin == null ? null : SpkiPin.parse(link.pin!);
    String hostname = link.hostname ?? link.baseUrl.host;
    String? warning;
    final probe = ref.read(gatewayFactoryProvider)(
      link.baseUrl,
      pin,
      res.token,
    );
    try {
      await probe.health();
      final info = await probe.info();
      if (info.host.isNotEmpty) hostname = info.host;
    } on Object catch (e) {
      warning =
          'Paired, but the first check failed: ${mapPairError(e).message}';
    } finally {
      probe.close();
    }
    if (gen != _gen) return;

    final hosts = ref.read(hostsProvider).value ?? const <Host>[];
    final old = replaceHostId == null
        ? null
        : hosts.where((h) => h.id == replaceHostId).firstOrNull;
    final host = Host(
      id: old?.id ?? const Uuid().v4(),
      label: old?.label ?? hostname,
      baseUrl: link.baseUrl,
      deviceId: res.deviceId,
      scope: res.scope.wire,
      pin: link.pin,
      hostname: hostname,
      addedAt: old?.addedAt ?? DateTime.now(),
      lastOk: warning == null ? DateTime.now() : null,
    );
    try {
      await ref.read(hostsProvider.notifier).add(host, res.token);
    } on Object catch (e) {
      if (gen != _gen) return;
      state = PairingState(
        phase: PairPhase.failed,
        link: link,
        failure: mapPairError(e),
      );
      return;
    }
    state = PairingState(
      phase: PairPhase.success,
      link: link,
      host: host,
      warning: warning,
    );
  }

  /// Gives up waiting. The request is aborted; a code the host already
  /// claimed stays burned on the host side.
  void cancel() {
    if (!state.busy) return;
    _gen++;
    _pending?.close();
    _pending = null;
    state = PairingState(phase: PairPhase.cancelled, link: state.link);
  }

  void reset() {
    if (state.busy) return;
    state = const PairingState();
  }
}
