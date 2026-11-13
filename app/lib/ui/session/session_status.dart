/// Status presentation helpers shared by the session widgets.
library;

import 'package:material_ui/material_ui.dart' hide View;

import '../../api/agentd_api.dart';

String statusLabel(View v) => switch (v.status) {
  Status.starting => 'starting',
  Status.working => 'working',
  Status.idle => 'idle',
  Status.waiting => 'needs you',
  Status.exited => v.exitCode == null ? 'exited' : 'exited ${v.exitCode}',
  Status.unknown => v.statusRaw.isEmpty ? 'unknown' : v.statusRaw,
};

Color statusColor(Status s, ColorScheme c) => switch (s) {
  Status.working => Colors.blue,
  Status.idle => Colors.green,
  Status.waiting => Colors.amber.shade800,
  Status.exited => c.error,
  _ => Colors.grey,
};

class StatusBadge extends StatelessWidget {
  const StatusBadge(this.view, {super.key});
  final View view;

  @override
  Widget build(BuildContext context) {
    final color = statusColor(view.status, Theme.of(context).colorScheme);
    return Semantics(
      label: 'Status ${statusLabel(view)}',
      child: Container(
        padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 2),
        decoration: BoxDecoration(
          color: color.withValues(alpha: 0.15),
          borderRadius: BorderRadius.circular(10),
          border: Border.all(color: color),
        ),
        child: Row(
          mainAxisSize: MainAxisSize.min,
          children: [
            if (view.status == Status.working)
              Padding(
                padding: const EdgeInsets.only(right: 6),
                child: SizedBox(
                  width: 10,
                  height: 10,
                  child: CircularProgressIndicator(
                    strokeWidth: 2,
                    color: color,
                  ),
                ),
              ),
            Text(
              statusLabel(view),
              style: TextStyle(color: color, fontSize: 12),
            ),
          ],
        ),
      ),
    );
  }
}
