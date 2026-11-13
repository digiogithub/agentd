/// Host-related providers. See README.md in this folder.
library;

import 'dart:async';
import 'dart:io';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:path_provider/path_provider.dart';

import '../api/agentd_api.dart';
import '../platform/deep_links.dart';
import '../platform/secret_store.dart';
import '../repositories/host.dart';
import '../repositories/host_store.dart';
import '../repositories/pairing_gateway.dart';

final secretStoreProvider = Provider<SecretStore>(
  (ref) => FlutterSecretStore(),
);

/// Directory holding `hosts.json`. Overridden in tests.
final appSupportDirProvider = FutureProvider<Directory>(
  (ref) => getApplicationSupportDirectory(),
);

final hostStoreProvider = FutureProvider<HostStore>((ref) async {
  final dir = await ref.watch(appSupportDirProvider.future);
  return HostStore(dir: dir, secrets: ref.watch(secretStoreProvider));
});

final gatewayFactoryProvider = Provider<GatewayFactory>(
  (ref) => defaultGatewayFactory,
);

/// A pairing link (it holds the one-time code) handed from a deep link to
/// the pairing screen, in memory only: never in a route's query string, where
/// it would end up in navigation state and logs. The screen takes it once.
final pendingPairLinkProvider = Provider<PendingPairLink>(
  (ref) => PendingPairLink(),
);

/// A plain holder (not provider state: it is taken from `initState`, where
/// modifying a provider is not allowed).
class PendingPairLink {
  String? _link;

  void set(String link) => _link = link;

  /// Returns the link and forgets it.
  String? take() {
    final l = _link;
    _link = null;
    return l;
  }
}

final deepLinkSourceProvider = Provider<DeepLinkSource>(
  (ref) => AppLinksSource(),
);

/// Paired hosts, in pairing order.
final hostsProvider = AsyncNotifierProvider<HostsNotifier, List<Host>>(
  HostsNotifier.new,
);

class HostsNotifier extends AsyncNotifier<List<Host>> {
  @override
  Future<List<Host>> build() async =>
      (await ref.watch(hostStoreProvider.future)).list();

  Future<HostStore> get _store => ref.read(hostStoreProvider.future);

  /// Stores [token] in the secret store and [host] in the metadata file.
  /// Throws [SecretStoreUnavailable] (nothing is stored then).
  Future<void> add(Host host, String token) async {
    await (await _store).add(host, token);
    state = AsyncData(await (await _store).list());
  }

  Future<void> rename(String id, String label) async {
    final host = (state.value ?? const []).where((h) => h.id == id);
    if (host.isEmpty || label.trim().isEmpty) return;
    await (await _store).update(host.first.copyWith(label: label.trim()));
    state = AsyncData(await (await _store).list());
  }

  Future<void> remove(String id) async {
    await (await _store).remove(id);
    state = AsyncData(await (await _store).list());
  }

  Future<void> markOk(String id) async {
    final host = (state.value ?? const []).where((h) => h.id == id);
    if (host.isEmpty) return;
    final last = host.first.lastOk;
    // Throttled: every write re-emits the host list.
    if (last != null && DateTime.now().difference(last).inMinutes < 5) return;
    await (await _store).update(host.first.copyWith(lastOk: DateTime.now()));
    state = AsyncData(await (await _store).list());
  }
}

/// What a host client depends on. Label, `lastOk`, scope... are not part of
/// it, so renaming a host or a health probe's `markOk` never rebuilds (and
/// closes) the client that sessions and feeds are using.
typedef HostCredentials = ({
  String id,
  Uri baseUrl,
  String? pin,
  String deviceId,
});

/// A configured client (pin, token) for a host. Rebuilt only when
/// [HostCredentials] change (re-pair, new URL or pin); the token is read from
/// the secret store once per build and never exposed in provider state.
final hostClientProvider = FutureProvider.family<AgentdClient, String>((
  ref,
  hostId,
) async {
  // Wait for the list to load (read: the select below is what we depend on).
  final hosts = await ref.read(hostsProvider.future);
  final cred = ref.watch(
    hostsProvider.select((a) {
      for (final h in a.value ?? const <Host>[]) {
        if (h.id == hostId) {
          return (
            id: h.id,
            baseUrl: h.baseUrl,
            pin: h.pin,
            deviceId: h.deviceId,
          );
        }
      }
      return null;
    }),
  );
  if (cred == null) throw StateError('unknown host $hostId');
  final label = hosts.where((h) => h.id == hostId).firstOrNull?.label ?? hostId;
  final token = await (await ref.watch(hostStoreProvider.future)).token(hostId);
  if (token == null) {
    throw ApiException(
      statusCode: 401,
      code: 'no_token',
      message: 'No device token stored for $label: re-pair.',
      local: true,
    );
  }
  final pin = cred.pin == null ? null : SpkiPin.parse(cred.pin!);
  final client = AgentdClient(baseUrl: cred.baseUrl, token: token, pin: pin);
  ref.onDispose(client.close);
  return client;
});

enum HostStatus { checking, online, offline, authError, pinMismatch }

/// One probe of a host (`health`, then `info` to validate the token).
typedef HostProbe = Future<HostStatus> Function(Ref ref, Host host);

Future<HostStatus> defaultHostProbe(Ref ref, Host host) async {
  try {
    final client = await ref.read(hostClientProvider(host.id).future);
    if (!await client.health()) return HostStatus.offline;
    await client.info();
    return HostStatus.online;
  } on PinMismatchException {
    return HostStatus.pinMismatch;
  } on ApiException catch (e) {
    return e.isUnauthenticated || e.isForbidden
        ? HostStatus.authError
        : HostStatus.offline;
  } on Object {
    return HostStatus.offline;
  }
}

final hostProbeProvider = Provider<HostProbe>((ref) => defaultHostProbe);

/// Base poll interval while online; offline hosts back off 2x up to 8x.
final healthIntervalProvider = Provider<Duration>(
  (ref) => const Duration(seconds: 15),
);

/// Periodic reachability of a host.
final hostStatusProvider = StreamProvider.autoDispose
    .family<HostStatus, String>((ref, hostId) async* {
      final base = ref.watch(healthIntervalProvider);
      final probe = ref.watch(hostProbeProvider);
      var failures = 0;
      var alive = true;
      Timer? timer;
      final sleeping = Completer<void>();
      ref.onDispose(() {
        alive = false;
        timer?.cancel();
        if (!sleeping.isCompleted) sleeping.complete();
      });
      yield HostStatus.checking;
      while (alive) {
        final hosts = ref.read(hostsProvider).value ?? const <Host>[];
        final matches = hosts.where((h) => h.id == hostId);
        if (matches.isEmpty) return;
        final s = await probe(ref, matches.first);
        if (!alive) return;
        yield s;
        if (s == HostStatus.online) {
          failures = 0;
          unawaited(ref.read(hostsProvider.notifier).markOk(hostId));
        } else {
          failures = failures < 3 ? failures + 1 : failures;
        }
        final factor = failures == 0 ? 1 : (1 << failures);
        final wake = Completer<void>();
        timer = Timer(base * factor, wake.complete);
        await Future.any([wake.future, sleeping.future]);
      }
    });
