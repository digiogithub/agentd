import 'dart:async';
import 'dart:io';

import 'package:agentd_app/api/agentd_api.dart';
import 'package:agentd_app/platform/deep_links.dart';
import 'package:agentd_app/platform/secret_store.dart';
import 'package:agentd_app/repositories/host.dart';
import 'package:agentd_app/repositories/host_store.dart';
import 'package:agentd_app/repositories/pairing_gateway.dart';
import 'package:agentd_app/state/agents.dart';
import 'package:agentd_app/state/providers.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

class FakeGateway implements PairingGateway {
  FakeGateway(this.owner, this.token);
  final FakeGateways owner;
  final String token;
  bool closed = false;

  @override
  Future<PairResult> pair({
    required String code,
    required String name,
    String? platform,
  }) {
    owner.pairCalls.add((code: code, name: name));
    return owner.pairResult.future;
  }

  @override
  Future<bool> health() async {
    if (owner.healthError != null) throw owner.healthError!;
    return true;
  }

  @override
  Future<Info> info() async => Info(host: owner.infoHost);

  @override
  void close() => closed = true;
}

class FakeGateways {
  // Lazy: created in the zone of the first use (the widget test's fake zone).
  late Completer<PairResult> pairResult = Completer();
  final pairCalls = <({String code, String name})>[];
  final created = <FakeGateway>[];
  Object? healthError;
  String infoHost = 'box';

  GatewayFactory get factory => (base, pin, token) {
    final g = FakeGateway(this, token);
    created.add(g);
    return g;
  };
}

class FakeDeepLinks implements DeepLinkSource {
  // ignore: close_sinks
  final controller = StreamController<Uri>.broadcast();
  Uri? initialLink;

  @override
  Future<Uri?> initial() async => initialLink;

  @override
  Stream<Uri> get links => controller.stream;
}

Host sampleHost({
  String id = 'h1',
  String label = 'box',
  String hostname = 'box.local',
  String? pin,
}) => Host(
  id: id,
  label: label,
  baseUrl: Uri.parse('https://box.example:7477'),
  deviceId: 'dev_$id',
  scope: 'write',
  hostname: hostname,
  addedAt: DateTime.utc(2026, 11, 1),
  pin: pin,
);

class TestEnv {
  TestEnv() : dir = Directory.systemTemp.createTempSync('agentd_app_test');
  final Directory dir;
  final secrets = MemorySecretStore();
  final gateways = FakeGateways();
  final links = FakeDeepLinks();

  HostStore get store => HostStore(dir: dir, secrets: secrets);

  List<dynamic> get overrides => [
    appSupportDirProvider.overrideWith((ref) async => dir),
    secretStoreProvider.overrideWithValue(secrets),
    gatewayFactoryProvider.overrideWithValue(gateways.factory),
    deepLinkSourceProvider.overrideWithValue(links),
    hostProbeProvider.overrideWithValue((ref, host) async => HostStatus.online),
  ];

  void dispose() {
    try {
      dir.deleteSync(recursive: true);
    } on Object {
      // ignore
    }
  }
}

ProviderContainer container(TestEnv env) => ProviderContainer(
  // ignore: argument_type_not_assignable
  overrides: env.overrides.cast(),
);

/// In-memory hosts list for widget tests (no file IO under FakeAsync).
class FakeHosts extends HostsNotifier {
  FakeHosts(this.initial);
  final List<Host> initial;

  @override
  Future<List<Host>> build() async => initial;

  @override
  Future<void> add(Host host, String token) async {
    final l = [...?state.value];
    l.removeWhere((h) => h.id == host.id);
    state = AsyncData([...l, host]);
  }

  @override
  Future<void> rename(String id, String label) async {
    state = AsyncData([
      for (final h in state.value!) h.id == id ? h.copyWith(label: label) : h,
    ]);
  }

  @override
  Future<void> remove(String id) async {
    state = AsyncData([
      for (final h in state.value!)
        if (h.id != id) h,
    ]);
  }

  @override
  Future<void> markOk(String id) async {}
}

List<dynamic> widgetOverrides(
  TestEnv env,
  List<Host> hosts, {
  Map<String, HostStatus> status = const {},
  bool failHosts = false,
}) => [
  hostsProvider.overrideWith(
    () => failHosts ? _FailingHosts() : FakeHosts(hosts),
  ),
  // Agent list: no network; every host has no sessions.
  hostFeedFactoryProvider.overrideWithValue(_emptyFeedFactory),
  reconnectTriggerProvider.overrideWithValue(const Stream.empty()),
  gatewayFactoryProvider.overrideWithValue(env.gateways.factory),
  deepLinkSourceProvider.overrideWithValue(env.links),
  hostProbeProvider.overrideWithValue(
    (ref, host) async => status[host.id] ?? HostStatus.online,
  ),
];

class _FailingHosts extends FakeHosts {
  _FailingHosts() : super(const []);
  @override
  Future<List<Host>> build() async => throw StateError('disk gone');
}

class _EmptyFeed implements HostFeed {
  @override
  Future<SessionList> list() async => const SessionList(sessions: []);
  @override
  Stream<SseEvent> events() => const Stream.empty();
  @override
  Stream<SseState> get states => const Stream.empty();
  @override
  void close() {}
}

Future<HostFeed> _emptyFeedFactory(Ref ref, Host host) async => _EmptyFeed();
