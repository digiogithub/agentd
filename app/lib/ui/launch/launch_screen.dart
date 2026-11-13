/// Launch agent (frontend-app.md section 1.2, screen 4): project, harness and
/// optional name from `GET /v1/info`. No free-form command / env.
library;

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:material_ui/material_ui.dart';

import '../../api/agentd_api.dart';
import '../../repositories/host.dart';
import '../../state/launch.dart';
import '../../state/providers.dart';

class LaunchScreen extends ConsumerStatefulWidget {
  const LaunchScreen({required this.hostId, super.key});
  final String hostId;

  @override
  ConsumerState<LaunchScreen> createState() => _LaunchScreenState();
}

class _LaunchScreenState extends ConsumerState<LaunchScreen> {
  final _form = GlobalKey<FormState>();
  final _target = TextEditingController();
  final _name = TextEditingController();
  final _custom = TextEditingController();
  String? _harness;

  @override
  void dispose() {
    _target.dispose();
    _name.dispose();
    _custom.dispose();
    super.dispose();
  }

  String _effectiveHarness(Info info) {
    if (info.canAdmin && _custom.text.trim().isNotEmpty) {
      return _custom.text.trim();
    }
    return _harness ?? (info.harnesses.isEmpty ? '' : info.harnesses.first);
  }

  String? _validateTarget(String? v, Info info) {
    final t = (v ?? '').trim();
    if (t.isEmpty) return 'Pick a project or type an absolute path';
    if (info.aliases.any((a) => a.name == t)) return null;
    if (!t.startsWith('/')) return 'Not an alias: use an absolute path';
    return null;
  }

  void _launch(Info info) {
    if (!(_form.currentState?.validate() ?? false)) return;
    final harness = _effectiveHarness(info);
    if (harness.isEmpty) return;
    final target = _target.text.trim();
    final typedName = _name.text.trim();
    final expected = typedName.isNotEmpty
        ? typedName
        : defaultSessionName(harness, target, info.aliases);
    ref
        .read(launchControllerProvider(widget.hostId).notifier)
        .launch(
          RunRequest(
            target: target,
            harness: harness,
            name: typedName.isEmpty ? null : typedName,
          ),
          expectedName: expected,
        );
  }

  @override
  Widget build(BuildContext context) {
    ref.listen<LaunchState>(launchControllerProvider(widget.hostId), (_, s) {
      if (s.phase == LaunchPhase.success && s.session != null) {
        final name = Uri.encodeComponent(s.session!.name);
        context.pushReplacement('/hosts/${widget.hostId}/sessions/$name');
      }
    });
    final hosts = ref.watch(hostsProvider).value ?? const <Host>[];
    final host = hosts.where((h) => h.id == widget.hostId).firstOrNull;
    final label = host?.label ?? widget.hostId;
    final info = ref.watch(hostInfoProvider(widget.hostId));
    return Scaffold(
      appBar: AppBar(title: Text('Launch agent on $label')),
      body: info.when(
        loading: () => const Center(child: CircularProgressIndicator()),
        error: (e, _) => _InfoError(
          error: e,
          onRetry: () => ref.invalidate(hostInfoProvider(widget.hostId)),
        ),
        data: (i) => _form_(context, i, label),
      ),
    );
  }

  Widget _form_(BuildContext context, Info info, String hostLabel) {
    final st = ref.watch(launchControllerProvider(widget.hostId));
    final canWrite = info.canWrite;
    final harness = _effectiveHarness(info);
    final typedName = _name.text.trim();
    final preview = typedName.isNotEmpty
        ? typedName
        : defaultSessionName(harness, _target.text, info.aliases);
    return Form(
      key: _form,
      child: ListView(
        padding: const EdgeInsets.all(16),
        children: [
          if (!canWrite)
            const Card(
              child: ListTile(
                leading: Icon(Icons.lock_outline),
                title: Text('Read-only device'),
                subtitle: Text(
                  'This device has no write scope on this host, so it cannot '
                  'launch agents.',
                ),
              ),
            ),
          Text('Project', style: Theme.of(context).textTheme.titleMedium),
          for (final a in info.aliases)
            ListTile(
              key: ValueKey('alias-${a.name}'),
              dense: true,
              contentPadding: EdgeInsets.zero,
              leading: Icon(
                _target.text.trim() == a.name
                    ? Icons.radio_button_checked
                    : Icons.radio_button_unchecked,
              ),
              title: Text(a.name),
              subtitle: Text(a.dir),
              onTap: () => setState(() => _target.text = a.name),
            ),
          TextFormField(
            key: const ValueKey('target'),
            controller: _target,
            decoration: const InputDecoration(
              labelText: 'Alias or absolute path on the host',
              hintText: '/home/user/src/project',
            ),
            autocorrect: false,
            validator: (v) => _validateTarget(v, info),
            onChanged: (_) => setState(() {}),
          ),
          const SizedBox(height: 20),
          Text('Harness', style: Theme.of(context).textTheme.titleMedium),
          if (info.harnesses.isEmpty && !info.canAdmin)
            const Padding(
              padding: EdgeInsets.symmetric(vertical: 8),
              child: Text('This host allows no harness for this device.'),
            ),
          Wrap(
            spacing: 8,
            children: [
              for (final h in info.harnesses)
                ChoiceChip(
                  key: ValueKey('harness-$h'),
                  label: Text(h),
                  selected: harness == h,
                  onSelected: (_) => setState(() {
                    _harness = h;
                    _custom.clear();
                  }),
                ),
            ],
          ),
          if (info.canAdmin)
            TextFormField(
              key: const ValueKey('custom-harness'),
              controller: _custom,
              decoration: const InputDecoration(
                labelText: 'Other harness (admin)',
                helperText: 'Any harness binary of the host; admin only.',
              ),
              autocorrect: false,
              onChanged: (_) => setState(() {}),
            ),
          const SizedBox(height: 20),
          TextFormField(
            key: const ValueKey('name'),
            controller: _name,
            decoration: InputDecoration(
              labelText: 'Name (optional)',
              hintText: preview.isEmpty ? '<harness>@<project>' : preview,
              helperText: preview.isEmpty
                  ? null
                  : 'Session name: ${typedName.isEmpty ? '$preview (default)' : preview}',
            ),
            autocorrect: false,
            validator: (v) {
              final t = (v ?? '').trim();
              if (t.isEmpty || sessionNameRe.hasMatch(t)) return null;
              return 'Letters, digits and . _ @ - only';
            },
            onChanged: (_) => setState(() {}),
          ),
          const SizedBox(height: 20),
          if (st.failure != null)
            Card(
              key: const ValueKey('launch-error'),
              color: Theme.of(context).colorScheme.errorContainer,
              child: Padding(
                padding: const EdgeInsets.all(12),
                child: Text(st.failure!.message),
              ),
            ),
          if (st.phase == LaunchPhase.outcomeUnknown) ...[
            OutlinedButton.icon(
              key: const ValueKey('check-outcome'),
              icon: const Icon(Icons.refresh),
              label: const Text('Refresh session list'),
              onPressed: () => ref
                  .read(launchControllerProvider(widget.hostId).notifier)
                  .checkOutcome(),
            ),
            TextButton(
              key: const ValueKey('discard-launch'),
              onPressed: () => ref
                  .read(launchControllerProvider(widget.hostId).notifier)
                  .reset(),
              child: const Text('Edit and launch again'),
            ),
          ] else
            FilledButton.icon(
              key: const ValueKey('launch'),
              icon: st.busy
                  ? const SizedBox(
                      width: 16,
                      height: 16,
                      child: CircularProgressIndicator(strokeWidth: 2),
                    )
                  : const Icon(Icons.rocket_launch_outlined),
              label: const Text('Launch'),
              onPressed: !canWrite || st.busy || harness.isEmpty
                  ? null
                  : () => _launch(info),
            ),
        ],
      ),
    );
  }
}

class _InfoError extends StatelessWidget {
  const _InfoError({required this.error, required this.onRetry});
  final Object error;
  final VoidCallback onRetry;

  @override
  Widget build(BuildContext context) {
    final msg =
        error is ApiException && (error as ApiException).isUnauthenticated
        ? 'This device is no longer authorized. Re-pair the host.'
        : 'Cannot load the host options.';
    return Center(
      child: Column(
        mainAxisSize: MainAxisSize.min,
        children: [
          Text(msg),
          const SizedBox(height: 12),
          OutlinedButton(onPressed: onRetry, child: const Text('Retry')),
        ],
      ),
    );
  }
}
