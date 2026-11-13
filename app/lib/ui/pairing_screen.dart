/// Pairing form and "waiting for approval" view.
library;

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:go_router/go_router.dart';
import 'package:material_ui/material_ui.dart';

import '../platform/device_info.dart';
import '../repositories/host.dart';
import '../repositories/pair_link.dart';
import '../state/pairing_controller.dart';
import '../state/providers.dart';
import 'pairing_scan_screen.dart';

class PairingScreen extends ConsumerStatefulWidget {
  const PairingScreen({this.initialLink, this.rehostId, super.key});

  /// A raw `agentd://pair?...` string to prefill from (deep link).
  final String? initialLink;

  /// Re-pair this host: URL and pin prefilled, code to be entered.
  final String? rehostId;

  @override
  ConsumerState<PairingScreen> createState() => _PairingScreenState();
}

class _PairingScreenState extends ConsumerState<PairingScreen> {
  final _link = TextEditingController();
  final _url = TextEditingController();
  final _code = TextEditingController();
  final _fp = TextEditingController();
  String? _hostname;
  String? _error;

  @override
  void initState() {
    super.initState();
    // A link from a deep link comes through memory, never the route.
    final raw = widget.initialLink ?? ref.read(pendingPairLinkProvider).take();
    if (raw != null) _applyLink(raw);
    _prefillRehost();
    // The hosts list may still be loading (cold start): prefill when it lands.
    if (widget.rehostId != null && !_prefilled) {
      ref.listenManual(hostsProvider, (_, next) {
        if (next.hasValue && !_prefilled && mounted) {
          setState(_prefillRehost);
        }
      });
    }
  }

  bool _prefilled = false;

  /// Re-pair starts from the host's own URL and pin.
  void _prefillRehost() {
    final h = _rehostHost();
    if (h == null) return;
    _prefilled = true;
    if (_url.text.isEmpty) _url.text = h.baseUrl.toString();
    if (_fp.text.isEmpty) _fp.text = h.pin ?? '';
    _hostname ??= h.label;
  }

  @override
  void dispose() {
    _link.dispose();
    _url.dispose();
    _code.dispose();
    _fp.dispose();
    super.dispose();
  }

  void _applyLink(String raw) {
    try {
      final l = PairLink.parse(raw);
      _url.text = l.baseUrl.toString();
      _code.text = l.code;
      _fp.text = l.pin ?? '';
      _hostname = l.hostname;
      _error = null;
    } on PairLinkException catch (e) {
      _error = e.message;
    }
  }

  Future<void> _scan() async {
    final raw = await Navigator.of(context).push<String>(
      MaterialPageRoute(builder: (_) => const PairingScanScreen()),
    );
    if (raw != null && mounted) setState(() => _applyLink(raw));
  }

  Host? _rehostHost() {
    final id = widget.rehostId;
    if (id == null) return null;
    final hosts = ref.read(hostsProvider).value ?? const <Host>[];
    return hosts.where((h) => h.id == id).firstOrNull;
  }

  /// Re-pairing keeps URL and pin; a change must be confirmed knowingly.
  Future<bool> _confirmEndpointChange(Host old, PairLink link) async {
    final dropsPin = old.pin != null && link.pin == null;
    final ok = await showDialog<bool>(
      context: context,
      builder: (c) => AlertDialog(
        key: const Key('endpoint-change-dialog'),
        title: const Text('Change where this host points?'),
        content: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text('Host: ${old.label}'),
            const SizedBox(height: 8),
            Text('Current URL: ${old.baseUrl}'),
            Text('New URL: ${link.baseUrl}'),
            const SizedBox(height: 8),
            Text('Current fingerprint: ${old.pin ?? 'none (system trust)'}'),
            Text('New fingerprint: ${link.pin ?? 'none (system trust)'}'),
            const SizedBox(height: 8),
            Text(
              dropsPin
                  ? 'WARNING: the new link has no fingerprint. Continuing '
                        'removes certificate pinning for this host.'
                  : 'Only continue if you moved or re-keyed this host '
                        'yourself.',
            ),
          ],
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(c, false),
            child: const Text('Cancel'),
          ),
          FilledButton(
            key: const Key('confirm-endpoint-change'),
            onPressed: () => Navigator.pop(c, true),
            child: const Text('Change and pair'),
          ),
        ],
      ),
    );
    return ok ?? false;
  }

  Future<void> _submit() async {
    final PairLink link;
    try {
      link = PairLink.fromFields(
        url: _url.text,
        code: _code.text,
        fp: _fp.text,
        hostname: _hostname,
      );
    } on PairLinkException catch (e) {
      setState(() => _error = e.message);
      return;
    }
    setState(() => _error = null);
    final ok = await showDialog<bool>(
      context: context,
      builder: (c) => AlertDialog(
        title: const Text('Pair with this host?'),
        content: Column(
          mainAxisSize: MainAxisSize.min,
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Text('Host: ${link.displayName}'),
            Text('URL: ${link.baseUrl}'),
            Text(
              link.pin == null
                  ? 'Certificate: system trust (no fingerprint)'
                  : 'Fingerprint: ${link.pin}',
            ),
            const SizedBox(height: 8),
            const Text(
              'Only continue if you started this on your own machine with '
              '"agentd serve pair".',
            ),
          ],
        ),
        actions: [
          TextButton(
            onPressed: () => Navigator.pop(c, false),
            child: const Text('Cancel'),
          ),
          FilledButton(
            onPressed: () => Navigator.pop(c, true),
            child: const Text('Pair'),
          ),
        ],
      ),
    );
    if (!(ok ?? false) || !mounted) return;
    var allow = false;
    final old = _rehostHost();
    if (old != null && endpointChanged(old, link)) {
      allow = await _confirmEndpointChange(old, link);
      if (!allow || !mounted) return;
    }
    await ref
        .read(pairingControllerProvider.notifier)
        .start(
          link,
          replaceHostId: widget.rehostId,
          allowEndpointChange: allow,
        );
  }

  @override
  Widget build(BuildContext context) {
    ref.listen(pairingControllerProvider, (prev, next) {
      if (next.phase == PairPhase.success) context.go('/');
    });
    final st = ref.watch(pairingControllerProvider);
    return Scaffold(
      appBar: AppBar(title: const Text('Pair host')),
      body: st.busy ? _waiting(st) : _form(st),
    );
  }

  Widget _waiting(PairingState st) {
    final name = st.link?.displayName ?? 'the host';
    return Center(
      child: Padding(
        padding: const EdgeInsets.all(24),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            const CircularProgressIndicator(semanticsLabel: 'Waiting'),
            const SizedBox(height: 16),
            Text(
              'Waiting for approval on $name…',
              style: Theme.of(context).textTheme.titleMedium,
            ),
            const SizedBox(height: 8),
            const Text('Approve this device in the terminal running agentd.'),
            const SizedBox(height: 16),
            OutlinedButton(
              onPressed: () =>
                  ref.read(pairingControllerProvider.notifier).cancel(),
              child: const Text('Cancel'),
            ),
          ],
        ),
      ),
    );
  }

  Widget _form(PairingState st) {
    final failure = st.failure;
    final msg = _error ?? failure?.message;
    final scheme = Theme.of(context).colorScheme;
    return ListView(
      padding: const EdgeInsets.all(16),
      children: [
        if (supportsQrScan)
          Padding(
            padding: const EdgeInsets.only(bottom: 16),
            child: FilledButton.icon(
              icon: const Icon(Icons.qr_code_scanner),
              label: const Text('Scan QR code'),
              onPressed: _scan,
            ),
          ),
        TextField(
          controller: _link,
          decoration: const InputDecoration(
            labelText: 'Paste agentd://pair link',
            helperText: 'Fills the fields below',
          ),
          onChanged: (v) {
            if (v.trim().startsWith('agentd://')) {
              setState(() => _applyLink(v));
            }
          },
        ),
        const SizedBox(height: 12),
        TextField(
          controller: _url,
          keyboardType: TextInputType.url,
          decoration: const InputDecoration(
            labelText: 'Host URL',
            hintText: 'https://box.tailnet.ts.net:7477',
          ),
        ),
        const SizedBox(height: 12),
        TextField(
          controller: _code,
          textCapitalization: TextCapitalization.characters,
          decoration: const InputDecoration(
            labelText: 'Pairing code',
            hintText: 'XXXX-XXXX-XXXX',
          ),
        ),
        const SizedBox(height: 12),
        TextField(
          controller: _fp,
          decoration: const InputDecoration(
            labelText: 'Certificate fingerprint (optional)',
            hintText: 'sha256:...',
          ),
        ),
        if (msg != null)
          Semantics(
            liveRegion: true,
            child: Padding(
              padding: const EdgeInsets.only(top: 16),
              child: Text(msg, style: TextStyle(color: scheme.error)),
            ),
          ),
        if (st.phase == PairPhase.cancelled)
          const Padding(
            padding: EdgeInsets.only(top: 16),
            child: Text(
              'Cancelled. The code may be used up: run "agentd serve pair" '
              'for a new one.',
            ),
          ),
        const SizedBox(height: 24),
        FilledButton(onPressed: _submit, child: const Text('Pair')),
      ],
    );
  }
}
