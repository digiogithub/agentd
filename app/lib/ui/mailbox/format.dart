/// Small formatting helpers of the mailbox screens.
library;

import '../../api/agentd_api.dart';

String messageTitle(Message m) {
  if (m.subject.trim().isNotEmpty) return m.subject.trim();
  final first = m.body.trim().split('\n').first;
  return first.isEmpty ? '(empty message)' : first;
}

String ago(DateTime? t, {DateTime? now}) {
  if (t == null) return '';
  final d = (now ?? DateTime.now()).difference(t);
  if (d.inMinutes < 1) return 'now';
  if (d.inHours < 1) return '${d.inMinutes}m';
  if (d.inDays < 1) return '${d.inHours}h';
  return '${d.inDays}d';
}
