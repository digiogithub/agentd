/// Agent list across hosts: per host an initial `GET /v1/sessions` plus a
/// resumable `/v1/events` stream, merged into one sorted list.
/// See README.md in this folder and frontend-app.md sections 2, 4.3, 4.4.
library;

import 'dart:async';
import 'dart:convert';

import 'package:connectivity_plus/connectivity_plus.dart';
import 'package:flutter/widgets.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../api/agentd_api.dart';
import '../api/json.dart';
import '../repositories/host.dart';
import 'agents_model.dart';
import 'providers.dart';

export 'agents_model.dart';

// ------------------------------------------------------------------ seams

/// What the list needs from one host. The default wraps [AgentdClient]; tests
/// provide fakes.
abstract class HostFeed {
  Future<SessionList> list();

  /// `/v1/events`; transport retries happen inside. A terminal failure is an
  /// error event followed by close.
  Stream<SseEvent> events();

  /// Connection state changes of [events].
  Stream<SseState> get states;

  void close();
}

typedef HostFeedFactory = Future<HostFeed> Function(Ref ref, Host host);

class ClientHostFeed implements HostFeed {
  /// [client] is shared ([hostClientProvider]) unless [ownsClient].
  ClientHostFeed(this.client, {this.ownsClient = false});
  final AgentdClient client;
  final bool ownsClient;
  final _states = StreamController<SseState>.broadcast(sync: true);
  StreamSubscription<SseState>? _stateSub;

  @override
  Future<SessionList> list() => client.listSessions();

  @override
  Stream<SseEvent> events() {
    final sse = client.eventsStream();
    unawaited(_stateSub?.cancel());
    _stateSub = sse.states.listen(_states.add);
    return sse.events();
  }

  @override
  Stream<SseState> get states => _states.stream;

  @override
  void close() {
    unawaited(_stateSub?.cancel());
    unawaited(_states.close());
    if (ownsClient) client.close();
  }
}

/// Shares the host's client ([hostClientProvider]); it is rebuilt only when
/// the credentials change, and the runner is restarted then (`_reconcile`).
Future<HostFeed> defaultHostFeed(Ref ref, Host host) async =>
    ClientHostFeed(await ref.read(hostClientProvider(host.id).future));

final hostFeedFactoryProvider = Provider<HostFeedFactory>(
  (ref) => defaultHostFeed,
);

/// Emits when every stream should reconnect: app resumed, or the network
/// changed (debounced 500 ms). Overridden in tests.
final reconnectTriggerProvider = Provider<Stream<void>>((ref) {
  final ctl = StreamController<void>.broadcast();
  Timer? debounce;
  AppLifecycleListener? life;
  StreamSubscription<List<ConnectivityResult>>? net;
  try {
    life = AppLifecycleListener(onResume: () => ctl.add(null));
    net = Connectivity().onConnectivityChanged.listen((_) {
      debounce?.cancel();
      debounce = Timer(const Duration(milliseconds: 500), () => ctl.add(null));
    }, onError: (Object _) {});
  } on Object {
    // No binding / platform plugin: resume still works when life was set.
  }
  ref.onDispose(() {
    debounce?.cancel();
    life?.dispose();
    unawaited(net?.cancel());
    unawaited(ctl.close());
  });
  return ctl.stream;
});

/// Wait between attempts to fetch the first list after a failure.
final agentsRetryProvider = Provider<Duration>(
  (ref) => const Duration(seconds: 5),
);

/// Minimum spacing of list refetches triggered by `view.changed` (keeps the
/// preview line fresh without a request per PTY write).
final agentsPreviewRefreshProvider = Provider<Duration>(
  (ref) => const Duration(seconds: 3),
);

// --------------------------------------------------------------- provider

final agentsProvider = NotifierProvider<AgentsNotifier, AgentsState>(
  AgentsNotifier.new,
);

class AgentsNotifier extends Notifier<AgentsState> {
  final _runners = <String, _HostRunner>{};
  final _seen = <String>{};
  List<Host> _hosts = const [];

  @override
  AgentsState build() {
    ref.listen(hostsProvider, (_, next) {
      if (next.hasValue) _reconcile(next.requireValue);
    });
    final trigger = ref.watch(reconnectTriggerProvider).listen((_) {
      reconnectAll();
    });
    ref.onDispose(() {
      unawaited(trigger.cancel());
      for (final r in _runners.values) {
        r.stop();
      }
      _runners.clear();
    });
    scheduleMicrotask(() {
      if (!ref.mounted) return;
      final h = ref.read(hostsProvider);
      if (h.hasValue) _reconcile(h.requireValue);
    });
    return const AgentsState();
  }

  Ref get ref0 => ref;

  Future<HostFeed> _createFeed(Host host) =>
      ref.read(hostFeedFactoryProvider)(ref, host);

  void _reconcile(List<Host> hosts) {
    final old = {for (final h in _hosts) h.id: h};
    _hosts = hosts;
    final ids = {for (final h in hosts) h.id};
    for (final id in _runners.keys.toList()) {
      if (!ids.contains(id)) _runners.remove(id)!.stop();
    }
    for (final h in hosts) {
      final prev = old[h.id];
      final r = _runners[h.id];
      final changed =
          prev != null &&
          (prev.baseUrl != h.baseUrl ||
              prev.pin != h.pin ||
              prev.deviceId != h.deviceId);
      if (r != null && !changed) continue;
      r?.stop();
      _runners[h.id] = _HostRunner(this, h)..start();
    }
    _publish();
  }

  /// Refetch every host and restart its stream (pull to refresh, resume).
  Future<void> refresh() =>
      Future.wait([for (final r in _runners.values.toList()) r.restart()]);

  /// Reconnect every host in parallel (resume, connectivity change).
  void reconnectAll() {
    for (final r in _runners.values.toList()) {
      unawaited(r.restart());
    }
  }

  /// The user opened the session: its attention dot clears locally until the
  /// daemon raises attention again.
  void markSeen(String hostId, String name) {
    if (_seen.add('$hostId/$name')) _publish();
  }

  void _publish() {
    if (!ref.mounted) return;
    final labels = {for (final h in _hosts) h.id: h.label};
    final entries = <AgentEntry>[];
    final infos = <String, HostFeedInfo>{};
    for (final h in _hosts) {
      final r = _runners[h.id];
      if (r == null) continue;
      infos[h.id] = r.info;
      for (final s in r.sessions.values) {
        entries.add(
          AgentEntry(
            hostId: h.id,
            hostLabel: labels[h.id] ?? h.id,
            session: s,
            hostOnline: r.info.online,
            attention:
                s.attention == true && !_seen.contains('${h.id}/${s.name}'),
            attentionReason: r.reasons[s.name],
          ),
        );
      }
    }
    entries.sort(compareAgents);
    state = AgentsState(
      entries: entries,
      hosts: infos,
      loading: infos.isEmpty
          ? ref.read(hostsProvider).isLoading
          : !infos.values.any((i) => i.loaded || i.error != null),
    );
  }
}

// ----------------------------------------------------------- per-host feed

class _HostRunner {
  _HostRunner(this.owner, this.host);

  final AgentsNotifier owner;
  final Host host;

  final sessions = <String, SessionSummary>{};

  /// Wire reason of the last attention event per session (turn_done, waiting,
  /// bell...), for notifications.
  final reasons = <String, String>{};
  HostFeedInfo info = const HostFeedInfo();

  HostFeed? _feed;
  StreamSubscription<SseEvent>? _eventSub;
  StreamSubscription<SseState>? _stateSub;
  Timer? _retry;
  Timer? _previewTimer;
  int _gen = 0;
  String? _epoch;
  bool _listing = false;
  bool _stopped = false;

  /// Events that arrive while the list is being fetched wait here.
  List<SseEvent>? _buffer;

  void start() => unawaited(_connect());

  Future<void> restart() async {
    if (_stopped) return;
    _teardown();
    await _connect();
  }

  void stop() {
    _stopped = true;
    _teardown();
  }

  void _teardown() {
    _gen++;
    _retry?.cancel();
    _previewTimer?.cancel();
    _previewTimer = null;
    unawaited(_eventSub?.cancel());
    unawaited(_stateSub?.cancel());
    _eventSub = null;
    _stateSub = null;
    _feed?.close();
    _feed = null;
    _listing = false;
    _buffer = null;
  }

  void _set(HostFeedInfo i) {
    info = i;
    owner._publish();
  }

  void _markOnline() {
    _set(
      HostFeedInfo(online: true, loaded: info.loaded, lastSeen: DateTime.now()),
    );
  }

  void _markOffline(Object e) {
    final auth = e is ApiException && (e.isUnauthenticated || e.isForbidden);
    _set(
      HostFeedInfo(
        loaded: info.loaded,
        lastSeen: info.lastSeen,
        authError: auth,
        error: switch (e) {
          PinMismatchException() => 'certificate pin mismatch',
          ApiException() => e.message,
          ConnectionException() => e.message,
          _ => '$e',
        },
      ),
    );
  }

  Future<void> _connect() async {
    final gen = ++_gen;
    HostFeed feed;
    try {
      feed = await owner._createFeed(host);
    } on Object catch (e) {
      if (gen == _gen && !_stopped) {
        _markOffline(e);
        _scheduleRetry(gen);
      }
      return;
    }
    if (gen != _gen || _stopped) {
      feed.close();
      return;
    }
    _feed = feed;
    _buffer = [];
    _stateSub = feed.states.listen((s) {
      if (gen != _gen) return;
      switch (s) {
        case SseState.connected:
          _markOnline();
          if (!info.loaded) unawaited(_fetch(gen));
        case SseState.reconnecting:
          _set(
            HostFeedInfo(
              loaded: info.loaded,
              lastSeen: info.lastSeen,
              error: 'connection lost',
            ),
          );
        case SseState.connecting || SseState.stopped:
          break;
      }
    });
    _eventSub = feed.events().listen(
      (e) => _onEvent(gen, e),
      onError: (Object e) {
        if (gen != _gen) return;
        _markOffline(e);
      },
    );
    await _fetch(gen);
  }

  void _scheduleRetry(int gen) {
    _retry?.cancel();
    _retry = Timer(owner.ref0.read(agentsRetryProvider), () {
      if (gen == _gen && !_stopped) unawaited(restart());
    });
  }

  /// Fetches the list and replaces this host's sessions with it.
  Future<void> _fetch(int gen) async {
    final feed = _feed;
    if (feed == null || _listing) return;
    _listing = true;
    _buffer ??= [];
    try {
      final list = await feed.list();
      if (gen != _gen) return;
      sessions
        ..clear()
        ..addEntries(list.sessions.map((s) => MapEntry(s.name, s)));
      info = HostFeedInfo(online: true, loaded: true, lastSeen: DateTime.now());
    } on Object catch (e) {
      if (gen != _gen) return;
      _listing = false;
      _flushBuffer(apply: false);
      _markOffline(e);
      final auth = e is ApiException && (e.isUnauthenticated || e.isForbidden);
      if (!auth) _scheduleRetry(gen);
      return;
    }
    _listing = false;
    _flushBuffer(apply: true);
    owner._publish();
  }

  void _flushBuffer({required bool apply}) {
    final buf = _buffer;
    _buffer = null;
    if (!apply || buf == null) return;
    for (final e in buf) {
      _apply(e);
    }
  }

  void _onEvent(int gen, SseEvent e) {
    if (gen != _gen) return;
    if (_listing) {
      (_buffer ??= []).add(e);
      return;
    }
    _apply(e);
    owner._publish();
  }

  Json _data(SseEvent e) {
    try {
      return asJson(jsonDecode(e.data));
    } on Object {
      return const {};
    }
  }

  void _apply(SseEvent e) {
    final j = _data(e);
    final data = asJson(j['data']);
    final name = jStringOrNull(j['session']);
    switch (e.event) {
      case 'hello':
        final epoch = jStringOrNull(data['epoch']) ?? jStringOrNull(j['epoch']);
        final changed = _epoch != null && epoch != null && epoch != _epoch;
        _epoch = epoch ?? _epoch;
        if (changed) unawaited(_fetch(_gen));
        if (!info.online) _markOnline();
      case 'resync':
        unawaited(_fetch(_gen));
      case 'session.created' || 'session.started':
        final s = SessionSummary.fromJson({
          ...data,
          'name': jStringOrNull(data['name']) ?? name ?? '',
        });
        if (s.name.isEmpty) return;
        sessions[s.name] = _merge(sessions[s.name], s);
        _seenReset(s.name);
      case 'session.exited':
        final cur = sessions[name];
        if (cur == null) return;
        sessions[cur.name] = _copy(
          cur,
          state: SessionState.exited,
          status: Status.exited,
          exitCode: jIntOrNull(data['exit_code']),
          attention: false,
        );
      case 'session.removed':
        if (name != null) sessions.remove(name);
      case 'status.changed':
        final cur = sessions[name];
        if (cur == null) return;
        final to = data['to'] ?? data['status'];
        final att = jBoolOrNull(data['attention']);
        sessions[cur.name] = _copy(
          cur,
          status: to == null ? null : Status.parse(to),
          attention: att,
          revision: jIntOrNull(data['revision']),
        );
        _noteReason(cur.name, data, att);
        if (att == true) _seenReset(cur.name);
      case 'attention.changed':
        final cur = sessions[name];
        final att = jBoolOrNull(data['attention']);
        if (cur == null || att == null) return;
        sessions[cur.name] = _copy(cur, attention: att);
        _noteReason(cur.name, data, att);
        if (att) _seenReset(cur.name);
      case 'input.changed':
        final cur = sessions[name];
        if (cur == null || data['state'] == null) return;
        sessions[cur.name] = _copy(
          cur,
          inputState: InputState.parse(data['state']),
        );
      case 'view.changed':
        final cur = sessions[name];
        if (cur == null) return;
        sessions[cur.name] = _copy(cur, revision: jIntOrNull(data['revision']));
        _schedulePreviewRefresh();
    }
  }

  void _noteReason(String name, Map<String, dynamic> data, bool? att) {
    if (att == false) {
      reasons.remove(name);
      return;
    }
    final r = data['reason'] ?? data['attention_reason'];
    if (att == true && r is String && r.isNotEmpty) reasons[name] = r;
  }

  void _seenReset(String name) => owner._seen.remove('${host.id}/$name');

  void _schedulePreviewRefresh() {
    if (_previewTimer != null) return;
    final gen = _gen;
    _previewTimer = Timer(owner.ref0.read(agentsPreviewRefreshProvider), () {
      _previewTimer = null;
      if (gen == _gen && !_stopped) unawaited(_fetch(gen));
    });
  }

  SessionSummary _merge(SessionSummary? old, SessionSummary s) =>
      old == null ? s : _copy(s, preview: s.preview ?? old.preview);

  SessionSummary _copy(
    SessionSummary s, {
    SessionState? state,
    Status? status,
    int? exitCode,
    bool? attention,
    int? revision,
    InputState? inputState,
    String? preview,
  }) => SessionSummary(
    name: s.name,
    state: state ?? s.state,
    stateRaw: state?.wire ?? s.stateRaw,
    pid: s.pid,
    cwd: s.cwd,
    cmd: s.cmd,
    profile: s.profile,
    exitCode: exitCode ?? s.exitCode,
    startedAt: s.startedAt,
    lastOutput: s.lastOutput,
    idleMs: s.idleMs,
    unread: s.unread,
    attached: s.attached,
    cols: s.cols,
    rows: s.rows,
    title: s.title,
    inputState: inputState ?? s.inputState,
    status: status ?? s.status,
    statusRaw: status?.wire ?? s.statusRaw,
    attention: attention ?? s.attention,
    revision: revision ?? s.revision,
    preview: preview ?? s.preview,
  );
}
