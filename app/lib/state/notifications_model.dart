/// Pure notification logic (AGED-US-0032): settings, attention transitions,
/// debounce / mute / visibility decisions, payloads and the tray model.
/// No Flutter or plugin imports, so it is unit-testable.
library;

import 'package:meta/meta.dart';

import 'agents_model.dart';

/// Why a notification is raised.
enum NotifyReason {
  waiting('waiting', 'is waiting for you'),
  turnDone('turn_done', 'finished its turn'),
  bell('bell', 'rang the bell'),
  exited('exited', 'exited');

  const NotifyReason(this.wire, this.text);
  final String wire;

  /// Short sentence, never screen content.
  final String text;

  /// Maps a wire attention reason (`notify` is treated like `waiting`).
  static NotifyReason fromAttention(String? raw, {required bool waiting}) {
    switch (raw) {
      case 'waiting' || 'notify':
        return NotifyReason.waiting;
      case 'turn_done':
        return NotifyReason.turnDone;
      case 'bell':
        return NotifyReason.bell;
    }
    return waiting ? NotifyReason.waiting : NotifyReason.turnDone;
  }
}

/// User settings, persisted as JSON.
@immutable
class NotifySettings {
  const NotifySettings({
    this.muted = false,
    this.mutedHosts = const {},
    this.includePreview = false,
    this.keepInTray = true,
    this.foregroundService = false,
  });

  factory NotifySettings.fromJson(Map<String, dynamic> j) => NotifySettings(
    muted: j['muted'] == true,
    mutedHosts: {
      if (j['muted_hosts'] is List)
        for (final h in j['muted_hosts'] as List)
          if (h is String) h,
    },
    includePreview: j['include_preview'] == true,
    keepInTray: j['keep_in_tray'] is bool ? j['keep_in_tray'] as bool : true,
    foregroundService: j['foreground_service'] == true,
  );

  /// Global mute (also toggled from the tray).
  final bool muted;

  /// Host ids whose notifications are muted.
  final Set<String> mutedHosts;

  /// Add the daemon's one-line preview to the body. Off by default: the text
  /// would otherwise carry screen content.
  final bool includePreview;

  /// Closing the window keeps the app in the tray (desktop).
  final bool keepInTray;

  /// Android "keep connected" service. Deferred: stored but not acted upon.
  final bool foregroundService;

  NotifySettings copyWith({
    bool? muted,
    Set<String>? mutedHosts,
    bool? includePreview,
    bool? keepInTray,
    bool? foregroundService,
  }) => NotifySettings(
    muted: muted ?? this.muted,
    mutedHosts: mutedHosts ?? this.mutedHosts,
    includePreview: includePreview ?? this.includePreview,
    keepInTray: keepInTray ?? this.keepInTray,
    foregroundService: foregroundService ?? this.foregroundService,
  );

  Map<String, dynamic> toJson() => {
    'muted': muted,
    'muted_hosts': mutedHosts.toList()..sort(),
    'include_preview': includePreview,
    'keep_in_tray': keepInTray,
    'foreground_service': foregroundService,
  };

  @override
  bool operator ==(Object other) =>
      other is NotifySettings &&
      other.muted == muted &&
      other.includePreview == includePreview &&
      other.keepInTray == keepInTray &&
      other.foregroundService == foregroundService &&
      other.mutedHosts.length == mutedHosts.length &&
      other.mutedHosts.containsAll(mutedHosts);

  @override
  int get hashCode => Object.hash(
    muted,
    includePreview,
    keepInTray,
    foregroundService,
    mutedHosts.length,
  );
}

/// A session that needs the user: input of [NotificationPolicy].
@immutable
class AttentionEvent {
  const AttentionEvent({
    required this.hostId,
    required this.hostLabel,
    required this.session,
    required this.reason,
    this.preview,
  });

  final String hostId;
  final String hostLabel;
  final String session;
  final NotifyReason reason;
  final String? preview;

  String get key => '$hostId/$session';
}

/// What to show.
@immutable
class NotificationRequest {
  const NotificationRequest({
    required this.id,
    required this.title,
    required this.body,
    required this.payload,
  });
  final int id;
  final String title;
  final String body;

  /// An `agentd://open?h=<host id>&s=<session>` link.
  final String payload;
}

/// Deep link carried by a notification (host by id: resolved first by
/// `routeForDeepLink`, so it is never ambiguous).
String notificationPayload(String hostId, String session) => Uri(
  scheme: 'agentd',
  host: 'open',
  queryParameters: {'h': hostId, 's': session},
).toString();

/// Turns agent-list snapshots into attention events. The first snapshot of a
/// host is only a baseline (no notification storm on start or reconnect).
class AttentionTracker {
  final _baselined = <String>{};
  final _prev = <String, ({bool attention, AgentStatus status})>{};

  List<AttentionEvent> update(AgentsState s) {
    final events = <AttentionEvent>[];
    final next = <String, ({bool attention, AgentStatus status})>{};
    for (final e in s.entries) {
      if (s.hosts[e.hostId]?.loaded != true) {
        final old = _prev[e.key];
        if (old != null) next[e.key] = old;
        continue;
      }
      final cur = (attention: e.attention, status: e.status);
      next[e.key] = cur;
      if (!_baselined.contains(e.hostId)) continue;
      final prev = _prev[e.key];
      if (e.attention && !(prev?.attention ?? false)) {
        events.add(
          _event(
            e,
            NotifyReason.fromAttention(
              e.attentionReason,
              waiting: e.status == AgentStatus.waiting,
            ),
          ),
        );
      } else if (e.status == AgentStatus.exited &&
          prev != null &&
          prev.status != AgentStatus.exited) {
        events.add(_event(e, NotifyReason.exited));
      }
    }
    for (final id in s.hosts.keys) {
      if (s.hosts[id]!.loaded) _baselined.add(id);
    }
    _prev
      ..clear()
      ..addAll(next);
    return events;
  }

  AttentionEvent _event(AgentEntry e, NotifyReason r) => AttentionEvent(
    hostId: e.hostId,
    hostLabel: e.hostLabel,
    session: e.name,
    reason: r,
    preview: e.session.preview,
  );
}

/// Decides whether to notify (stateful only for the debounce).
class NotificationPolicy {
  NotificationPolicy({
    this.debounce = const Duration(seconds: 30),
    DateTime Function()? now,
  }) : _now = now ?? DateTime.now;

  final Duration debounce;
  final DateTime Function() _now;
  final _last = <String, DateTime>{};
  int _nextId = 1;

  /// The request to show, or null (muted, visible session, debounced).
  /// [visibleKey] is `<hostId>/<session>` of the session open and visible.
  NotificationRequest? decide(
    AttentionEvent e,
    NotifySettings settings, {
    String? visibleKey,
  }) {
    if (settings.muted || settings.mutedHosts.contains(e.hostId)) return null;
    if (visibleKey != null && visibleKey == e.key) return null;
    final now = _now();
    final k = '${e.key}#${e.reason.wire}';
    final last = _last[k];
    if (last != null && now.difference(last) < debounce) return null;
    _last[k] = now;
    _last.removeWhere((_, t) => now.difference(t) > debounce * 4);
    var body = '${e.session} ${e.reason.text}';
    final p = e.preview?.trim() ?? '';
    if (settings.includePreview && p.isNotEmpty) body = '$body: $p';
    return NotificationRequest(
      id: _nextId++ & 0x7fffffff,
      title: '${e.session} on ${e.hostLabel}',
      body: body,
      payload: notificationPayload(e.hostId, e.session),
    );
  }
}

/// `<hostId>/<session>` of a `/hosts/:id/sessions/:name` location, else null.
String? sessionKeyOfLocation(String location) {
  final seg = Uri.parse(location).pathSegments;
  if (seg.length == 4 && seg[0] == 'hosts' && seg[2] == 'sessions') {
    return '${seg[1]}/${seg[3]}';
  }
  return null;
}

enum TrayAction { open, toggleMute, quit, session }

@immutable
class TrayItem {
  const TrayItem(this.action, this.label, {this.hostId, this.session});
  final TrayAction action;
  final String label;
  final String? hostId;
  final String? session;
}

/// Tray icon count and menu.
@immutable
class TrayModel {
  const TrayModel({
    required this.count,
    required this.items,
    required this.tooltip,
  });

  /// Sessions needing attention.
  final int count;

  /// `Open`, the attention sessions, `Mute notifications` (checked label
  /// says which way it toggles), `Quit`.
  final List<TrayItem> items;
  final String tooltip;

  static const maxSessions = 10;

  factory TrayModel.from(AgentsState s, NotifySettings settings) {
    final att = [
      for (final e in s.entries)
        if (e.attention) e,
    ];
    return TrayModel(
      count: att.length,
      tooltip: att.isEmpty
          ? 'agentd'
          : 'agentd: ${att.length} need${att.length == 1 ? 's' : ''} attention',
      items: [
        const TrayItem(TrayAction.open, 'Open agentd'),
        for (final e in att.take(maxSessions))
          TrayItem(
            TrayAction.session,
            '${e.name} on ${e.hostLabel}',
            hostId: e.hostId,
            session: e.name,
          ),
        TrayItem(
          TrayAction.toggleMute,
          settings.muted ? 'Unmute notifications' : 'Mute notifications',
        ),
        const TrayItem(TrayAction.quit, 'Quit'),
      ],
    );
  }

  @override
  bool operator ==(Object other) =>
      other is TrayModel &&
      other.count == count &&
      other.tooltip == tooltip &&
      other.items.length == items.length &&
      [
        for (var i = 0; i < items.length; i++)
          items[i].label == other.items[i].label &&
              items[i].action == other.items[i].action,
      ].every((b) => b);

  @override
  int get hashCode => Object.hash(count, tooltip, items.length);
}
