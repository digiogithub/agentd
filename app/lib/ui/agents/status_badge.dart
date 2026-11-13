/// Status badge of an agent row: colour + icon + text (never colour alone).
library;

import 'package:material_ui/material_ui.dart';

import '../../state/agents_model.dart';

class StatusStyle {
  const StatusStyle(this.label, this.icon, this.color);
  final String label;
  final IconData icon;
  final MaterialColor color;
}

StatusStyle statusStyle(AgentStatus s, {int? exitCode, String? raw}) =>
    switch (s) {
      AgentStatus.waiting => const StatusStyle(
        'Waiting',
        Icons.front_hand_outlined,
        Colors.amber,
      ),
      AgentStatus.working => const StatusStyle(
        'Working',
        Icons.autorenew,
        Colors.blue,
      ),
      AgentStatus.idle => const StatusStyle(
        'Idle',
        Icons.check_circle_outline,
        Colors.green,
      ),
      AgentStatus.starting => const StatusStyle(
        'Starting',
        Icons.hourglass_top,
        Colors.grey,
      ),
      AgentStatus.exited => StatusStyle(
        exitCode == null || exitCode == 0 ? 'Exited' : 'Exited $exitCode',
        Icons.stop_circle_outlined,
        exitCode == null || exitCode == 0 ? Colors.grey : Colors.red,
      ),
      AgentStatus.unknown => StatusStyle(
        raw == null || raw.isEmpty ? 'Unknown' : raw,
        Icons.help_outline,
        Colors.grey,
      ),
    };

class StatusBadge extends StatelessWidget {
  const StatusBadge({
    required this.status,
    this.exitCode,
    this.raw,
    this.dimmed = false,
    super.key,
  });

  final AgentStatus status;
  final int? exitCode;
  final String? raw;
  final bool dimmed;

  @override
  Widget build(BuildContext context) {
    final st = statusStyle(status, exitCode: exitCode, raw: raw);
    final dark = Theme.of(context).brightness == Brightness.dark;
    final fg = dimmed
        ? Theme.of(context).disabledColor
        : (dark ? st.color.shade200 : st.color.shade900);
    return ExcludeSemantics(
      child: Container(
        padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 3),
        decoration: BoxDecoration(
          color: st.color.withValues(alpha: dark ? 0.22 : 0.16),
          borderRadius: BorderRadius.circular(12),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(st.icon, size: 14, color: fg),
            const SizedBox(width: 4),
            Text(
              st.label,
              style: Theme.of(context).textTheme.labelSmall
                  ?.copyWith(color: fg, fontWeight: FontWeight.w600),
            ),
          ],
        ),
      ),
    );
  }
}
