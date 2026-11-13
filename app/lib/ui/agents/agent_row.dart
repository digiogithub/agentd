/// One row of the agent list.
library;

import 'package:material_ui/material_ui.dart';

import '../../state/agents_model.dart';
import 'status_badge.dart';

/// "now", "5m ago", "3h ago", "2d ago".
String relativeTime(DateTime? t, DateTime now) {
  if (t == null) return '';
  final d = now.difference(t);
  if (d.inSeconds < 45) return 'now';
  if (d.inMinutes < 60) return '${d.inMinutes < 1 ? 1 : d.inMinutes}m ago';
  if (d.inHours < 24) return '${d.inHours}h ago';
  return '${d.inDays}d ago';
}

class AgentRow extends StatelessWidget {
  const AgentRow({
    required this.entry,
    required this.now,
    required this.onTap,
    super.key,
  });

  final AgentEntry entry;
  final DateTime now;
  final VoidCallback onTap;

  @override
  Widget build(BuildContext context) {
    final theme = Theme.of(context);
    final e = entry;
    final s = e.session;
    final offline = !e.hostOnline;
    final preview = (s.preview ?? '').replaceAll(RegExp(r'\s+'), ' ').trim();
    final when = relativeTime(e.lastActivity, now);
    final meta = [
      e.hostLabel,
      if (e.project.isNotEmpty) e.project,
      if (when.isNotEmpty) when,
    ].join(' · ');
    final st = statusStyle(e.status, exitCode: s.exitCode, raw: s.statusRaw);
    final label = [
      s.name,
      'on ${e.hostLabel}',
      st.label,
      if (e.attention) 'needs attention',
      if (offline) 'host offline',
      if (when.isNotEmpty) 'last activity $when',
    ].join(', ');
    final muted = theme.colorScheme.onSurfaceVariant;
    return Semantics(
      label: label,
      button: true,
      excludeSemantics: true,
      onTap: onTap,
      child: Opacity(
        opacity: offline ? 0.55 : 1,
        child: InkWell(
          onTap: onTap,
          child: Padding(
            padding: const EdgeInsets.symmetric(horizontal: 16, vertical: 10),
            child: Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                SizedBox(
                  width: 14,
                  child: Padding(
                    padding: const EdgeInsets.only(top: 6),
                    child: e.attention
                        ? Container(
                            key: const Key('attention-dot'),
                            width: 10,
                            height: 10,
                            decoration: const BoxDecoration(
                              color: Colors.red,
                              shape: BoxShape.circle,
                            ),
                          )
                        : null,
                  ),
                ),
                const SizedBox(width: 8),
                Expanded(
                  child: Column(
                    crossAxisAlignment: CrossAxisAlignment.start,
                    children: [
                      Text(
                        s.name,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: theme.textTheme.titleMedium?.copyWith(
                          fontWeight: e.attention
                              ? FontWeight.w700
                              : FontWeight.w500,
                        ),
                      ),
                      Text(
                        offline ? '$meta · host offline' : meta,
                        maxLines: 1,
                        overflow: TextOverflow.ellipsis,
                        style: theme.textTheme.bodySmall?.copyWith(
                          color: muted,
                        ),
                      ),
                      if (preview.isNotEmpty)
                        Padding(
                          padding: const EdgeInsets.only(top: 2),
                          child: Text(
                            preview,
                            maxLines: 1,
                            overflow: TextOverflow.ellipsis,
                            style: theme.textTheme.bodyMedium,
                          ),
                        ),
                    ],
                  ),
                ),
                const SizedBox(width: 8),
                StatusBadge(
                  status: e.status,
                  exitCode: s.exitCode,
                  raw: s.statusRaw,
                  dimmed: offline,
                ),
              ],
            ),
          ),
        ),
      ),
    );
  }
}
