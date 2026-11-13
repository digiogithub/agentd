import 'package:flutter/widgets.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';

import 'ui/app_shell.dart';

void main() {
  runApp(const ProviderScope(child: AgentdApp()));
}
