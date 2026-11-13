import 'dart:async';
import 'dart:convert';

import 'package:agentd_app/api/agentd_api.dart';
import 'package:agentd_app/repositories/host.dart';
import 'package:agentd_app/state/agents.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'package:agentd_app/state/providers.dart';

import 'fakes.dart';

class FakeFeed implements HostFeed {
  FakeFeed(this.owner, this.hostId);
  final FakeFeeds owner;
  final String hostId;
  // ignore: close_sinks
  final events$ = StreamController<SseEvent>();
  // ignore: close_sinks
  final states$ = StreamController<SseState>.broadcast();
  bool closed = false;
  int listCalls = 0;

  @override
  Future<SessionList> list() async {
    listCalls++;
    final err = owner.listError[hostId];
    if (err != null) throw err;
    return SessionList(sessions: [...?owner.lists[hostId]]);
  }

  @override
  Stream<SseEvent> events() => events$.stream;

  @override
  Stream<SseState> get states => states$.stream;

  @override
  void close() {
    closed = true;
  }

  int _n = 0;
  void emit(String event, {String? session, Map<String, Object?>? data}) {
    events$.add(
      SseEvent(
        id: 'e1:${++_n}',
        event: event,
        data: jsonEncode({'session': ?session, 'data': ?data}),
      ),
    );
  }
}

class FakeFeeds {
  final lists = <String, List<SessionSummary>>{};
  final listError = <String, Object>{};
  final feeds = <String, List<FakeFeed>>{};
  Object? factoryError;

  FakeFeed last(String hostId) => feeds[hostId]!.last;

  HostFeedFactory get factory => (Ref ref, Host host) async {
    if (factoryError != null) throw factoryError!;
    final f = FakeFeed(this, host.id);
    feeds.putIfAbsent(host.id, () => []).add(f);
    return f;
  };
}

SessionSummary sess(
  String name, {
  Status? status = Status.idle,
  SessionState state = SessionState.running,
  bool? attention,
  String cwd = '/srv/app',
  String? preview,
  DateTime? lastOutput,
  int? exitCode,
}) => SessionSummary(
  name: name,
  state: state,
  stateRaw: state.wire,
  status: status,
  statusRaw: status?.wire,
  attention: attention,
  cwd: cwd,
  preview: preview,
  lastOutput: lastOutput,
  exitCode: exitCode,
);

Future<void> settle([int n = 6]) async {
  for (var i = 0; i < n; i++) {
    await Future<void>.delayed(Duration.zero);
  }
}

List<dynamic> agentsOverrides(
  FakeFeeds feeds,
  List<Host> hosts, {
  Stream<void>? trigger,
  HostFeedFactory? factory,
}) => [
  hostsProvider.overrideWith(() => FakeHosts(hosts)),
  hostFeedFactoryProvider.overrideWithValue(factory ?? feeds.factory),
  reconnectTriggerProvider.overrideWithValue(trigger ?? const Stream.empty()),
  agentsRetryProvider.overrideWithValue(const Duration(milliseconds: 20)),
  agentsPreviewRefreshProvider.overrideWithValue(
    const Duration(milliseconds: 20),
  ),
];
