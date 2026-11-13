/// Pure model of the agent list: entries, project name, ordering.
library;

import 'package:meta/meta.dart';

import '../api/agentd_api.dart';

/// UI-facing status of a row (frontend-app.md section 2.1).
enum AgentStatus {
  waiting,
  working,
  idle,
  starting,
  exited,

  /// A status value this app does not know (forward compatibility).
  unknown,
}

/// Maps the wire status (falling back on the lifecycle state) to the badge.
AgentStatus agentStatusOf(SessionSummary s) {
  switch (s.status) {
    case Status.waiting:
      return AgentStatus.waiting;
    case Status.working:
      return AgentStatus.working;
    case Status.idle:
      return AgentStatus.idle;
    case Status.starting:
      return AgentStatus.starting;
    case Status.exited:
      return AgentStatus.exited;
    case Status.unknown || null:
      break;
  }
  return switch (s.state) {
    SessionState.exited => AgentStatus.exited,
    SessionState.starting => AgentStatus.starting,
    _ => AgentStatus.unknown,
  };
}

/// Project of a session: the part after `@` in its name (`codex@webapp` ->
/// `webapp`), else the basename of its working directory.
String projectOf(SessionSummary s) {
  final at = s.name.indexOf('@');
  if (at >= 0 && at < s.name.length - 1) return s.name.substring(at + 1);
  final parts = s.cwd.split('/').where((p) => p.isNotEmpty).toList();
  return parts.isEmpty ? '' : parts.last;
}

@immutable
class AgentEntry {
  const AgentEntry({
    required this.hostId,
    required this.hostLabel,
    required this.session,
    required this.hostOnline,
    required this.attention,
    this.attentionReason,
  });

  final String hostId;
  final String hostLabel;
  final SessionSummary session;
  final bool hostOnline;

  /// `attention` from the daemon, minus what the user already opened.
  final bool attention;

  /// Wire reason of the attention (`turn_done`, `waiting`, `bell`...), when
  /// the stream reported one.
  final String? attentionReason;

  String get key => '$hostId/${session.name}';
  String get name => session.name;
  String get project => projectOf(session);
  AgentStatus get status => agentStatusOf(session);
  DateTime? get lastActivity => session.lastOutput ?? session.startedAt;
}

int _rank(AgentEntry e) => switch (e.status) {
  AgentStatus.waiting => 1,
  AgentStatus.working => 2,
  AgentStatus.idle => 3,
  AgentStatus.starting => 4,
  AgentStatus.exited => 5,
  AgentStatus.unknown => 5,
};

/// Attention first, then waiting, working, idle, starting, exited; then most
/// recent activity; then host label and name for a stable order.
int compareAgents(AgentEntry a, AgentEntry b) {
  if (a.attention != b.attention) return a.attention ? -1 : 1;
  final r = _rank(a).compareTo(_rank(b));
  if (r != 0) return r;
  final ta = a.lastActivity;
  final tb = b.lastActivity;
  if (ta != tb) {
    if (ta == null) return 1;
    if (tb == null) return -1;
    return tb.compareTo(ta);
  }
  final h = a.hostLabel.compareTo(b.hostLabel);
  return h != 0 ? h : a.name.compareTo(b.name);
}

/// Connection state of one host as seen by the list.
@immutable
class HostFeedInfo {
  const HostFeedInfo({
    this.online = false,
    this.loaded = false,
    this.error,
    this.lastSeen,
    this.authError = false,
  });

  /// Stream connected or the last request answered.
  final bool online;

  /// The first session list arrived.
  final bool loaded;

  /// Why the host is offline, when known.
  final String? error;
  final DateTime? lastSeen;

  /// 401/403: the token is gone, the user must re-pair.
  final bool authError;
}

@immutable
class AgentsState {
  const AgentsState({
    this.entries = const [],
    this.hosts = const {},
    this.loading = true,
  });

  /// Sorted with [compareAgents].
  final List<AgentEntry> entries;
  final Map<String, HostFeedInfo> hosts;

  /// No host has answered yet.
  final bool loading;

  bool hostOnline(String hostId) => hosts[hostId]?.online ?? false;
}
