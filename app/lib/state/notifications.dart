/// Notification, tray and settings providers (AGED-US-0032). See README.md.
library;

import 'dart:async';
import 'dart:convert';
import 'dart:io';

import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../platform/notifications_platform.dart';
import 'agents.dart';
import 'notifications_model.dart';
import 'providers.dart';

// ------------------------------------------------------------- seams

final localNotifierProvider = Provider<LocalNotifier>(
  (ref) => PluginLocalNotifier(),
);
final trayServiceProvider = Provider<TrayService>((ref) {
  final t = PluginTrayService();
  ref.onDispose(() => unawaited(t.dispose()));
  return t;
});
final windowServiceProvider = Provider<WindowService>(
  (ref) => PluginWindowService(),
);
final linkOpenerProvider = Provider<LinkOpener>((ref) => UrlLauncherOpener());

/// True on desktop (tray and hide-on-close apply). Overridden in tests.
final isDesktopProvider = Provider<bool>((ref) => isDesktop);

/// True on Android (permission flow, ntfy help). Overridden in tests.
final isAndroidProvider = Provider<bool>((ref) => Platform.isAndroid);

/// Debounce window per session + reason.
final notifyDebounceProvider = Provider<Duration>(
  (ref) => const Duration(seconds: 30),
);

// ------------------------------------------------------------- settings

/// `notifications.json` in the app support dir (no secrets).
class NotifySettingsStore {
  NotifySettingsStore(this.dir);
  final Directory dir;

  File get _file => File('${dir.path}/notifications.json');

  Future<NotifySettings> load() async {
    try {
      if (!await _file.exists()) return const NotifySettings();
      final j = jsonDecode(await _file.readAsString());
      return j is Map<String, dynamic>
          ? NotifySettings.fromJson(j)
          : const NotifySettings();
    } on Object {
      return const NotifySettings();
    }
  }

  Future<void> save(NotifySettings s) async {
    await dir.create(recursive: true);
    final tmp = File('${_file.path}.tmp');
    await tmp.writeAsString(jsonEncode(s.toJson()));
    await tmp.rename(_file.path);
  }
}

final notifySettingsStoreProvider = FutureProvider<NotifySettingsStore>((
  ref,
) async {
  final dir = await ref.watch(appSupportDirProvider.future);
  return NotifySettingsStore(dir);
});

class NotifySettingsNotifier extends Notifier<NotifySettings> {
  bool _touched = false;

  @override
  NotifySettings build() {
    unawaited(_load());
    return const NotifySettings();
  }

  Future<void> _load() async {
    try {
      final store = await ref.read(notifySettingsStoreProvider.future);
      final s = await store.load();
      if (ref.mounted && !_touched) state = s;
    } on Object {
      // defaults
    }
  }

  Future<void> update(NotifySettings Function(NotifySettings) f) async {
    _touched = true;
    state = f(state);
    try {
      final store = await ref.read(notifySettingsStoreProvider.future);
      await store.save(state);
    } on Object {
      // keep the in-memory value; the next change retries
    }
  }

  Future<void> setMuted(bool v) => update((s) => s.copyWith(muted: v));
  Future<void> setIncludePreview(bool v) =>
      update((s) => s.copyWith(includePreview: v));
  Future<void> setKeepInTray(bool v) =>
      update((s) => s.copyWith(keepInTray: v));
  Future<void> setForegroundService(bool v) =>
      update((s) => s.copyWith(foregroundService: v));
  Future<void> setHostMuted(String hostId, bool v) => update(
    (s) => s.copyWith(
      mutedHosts: v
          ? {...s.mutedHosts, hostId}
          : ({...s.mutedHosts}..remove(hostId)),
    ),
  );
}

final notifySettingsProvider =
    NotifierProvider<NotifySettingsNotifier, NotifySettings>(
      NotifySettingsNotifier.new,
    );

// ------------------------------------------------------------- visibility

/// `<hostId>/<session>` of the session screen on top, or null.
class OpenSessionNotifier extends Notifier<String?> {
  @override
  String? build() => null;
  void set(String? key) => state = key;
}

final openSessionProvider = NotifierProvider<OpenSessionNotifier, String?>(
  OpenSessionNotifier.new,
);

/// The app window is in the foreground.
class AppVisibleNotifier extends Notifier<bool> {
  @override
  bool build() => true;
  void set(bool v) => state = v;
}

final appVisibleProvider = NotifierProvider<AppVisibleNotifier, bool>(
  AppVisibleNotifier.new,
);

// ------------------------------------------------------------- tray model

final trayModelProvider = Provider<TrayModel>(
  (ref) => TrayModel.from(
    ref.watch(agentsProvider),
    ref.watch(notifySettingsProvider),
  ),
);

// ------------------------------------------------------------- controller

/// Android 13+ permission state for the settings screen.
enum NotifyPermission { unknown, granted, denied }

class NotifyPermissionNotifier extends Notifier<NotifyPermission> {
  @override
  NotifyPermission build() {
    unawaited(refresh());
    return NotifyPermission.unknown;
  }

  Future<void> refresh() async {
    final ok = await ref.read(localNotifierProvider).permissionGranted();
    if (ref.mounted) {
      state = ok ? NotifyPermission.granted : NotifyPermission.denied;
    }
  }

  /// Asks the system (after the rationale dialog).
  Future<void> request() async {
    final ok = await ref.read(localNotifierProvider).requestPermission();
    if (ref.mounted) {
      state = ok ? NotifyPermission.granted : NotifyPermission.denied;
    }
  }
}

final notifyPermissionProvider =
    NotifierProvider<NotifyPermissionNotifier, NotifyPermission>(
      NotifyPermissionNotifier.new,
    );

/// Taps on notifications and tray entries, as `agentd://open` links, for the
/// router (same path as deep links).
final notificationTapsProvider = Provider<Stream<Uri>>((ref) {
  final c = ref.watch(notificationControllerProvider);
  return c.links;
});

final notificationControllerProvider = Provider<NotificationController>((ref) {
  final c = NotificationController(ref);
  ref.onDispose(c.dispose);
  return c;
});

/// Glue: agents state -> notifications, tray, window.
class NotificationController {
  NotificationController(this._ref) {
    _policy = NotificationPolicy(debounce: _ref.read(notifyDebounceProvider));
    final notifier = _ref.read(localNotifierProvider);
    _tapSub = notifier.taps.listen(_onTap);
    unawaited(
      notifier.init().then((_) async {
        final p = await notifier.launchPayload();
        if (p != null) _onTap(p);
      }),
    );
    _ref.listen(agentsProvider, (_, next) => _onAgents(next));
    if (_ref.read(isDesktopProvider)) {
      _initDesktop();
    }
  }

  final Ref _ref;
  late final NotificationPolicy _policy;
  final _tracker = AttentionTracker();
  final _pending = <Uri>[];
  late final StreamController<Uri> _links = StreamController<Uri>.broadcast(
    onListen: () => scheduleMicrotask(() {
      for (final u in _pending.toList()) {
        _links.add(u);
      }
      _pending.clear();
    }),
  );
  StreamSubscription<String>? _tapSub;

  Stream<Uri> get links => _links.stream;

  void _initDesktop() {
    final win = _ref.read(windowServiceProvider);
    unawaited(
      win
          .init(onClose: _onWindowClose)
          .then(
            (_) => win.setKeepInTray(
              _ref.read(notifySettingsProvider).keepInTray &&
                  _ref.read(trayServiceProvider).supported,
            ),
          ),
    );
    _ref.listen(notifySettingsProvider, (prev, next) {
      if (prev?.keepInTray != next.keepInTray) {
        unawaited(
          win.setKeepInTray(
            next.keepInTray && _ref.read(trayServiceProvider).supported,
          ),
        );
      }
    });
    _ref.listen(trayModelProvider, (_, next) => _pushTray(next));
    scheduleMicrotask(() => _pushTray(_ref.read(trayModelProvider)));
  }

  Future<void> _onWindowClose() async {
    final win = _ref.read(windowServiceProvider);
    final keep =
        _ref.read(notifySettingsProvider).keepInTray &&
        _ref.read(trayServiceProvider).supported;
    if (keep) {
      await win.hide();
      _ref.read(appVisibleProvider.notifier).set(false);
    } else {
      await win.quit();
    }
  }

  void _pushTray(TrayModel m) {
    final tray = _ref.read(trayServiceProvider);
    unawaited(
      tray.update(
        count: m.count,
        tooltip: m.tooltip,
        entries: [
          for (var i = 0; i < m.items.length; i++)
            TrayEntry(
              '$i',
              m.items[i].label,
              separatorBefore:
                  m.items[i].action == TrayAction.toggleMute ||
                  m.items[i].action == TrayAction.quit,
            ),
        ],
        onSelect: (id) => _onTrayEntry(m.items[int.parse(id)]),
      ),
    );
  }

  void _onTrayEntry(TrayItem item) {
    switch (item.action) {
      case TrayAction.open:
        unawaited(_showWindow());
      case TrayAction.session:
        unawaited(_showWindow());
        _emit(Uri.parse(notificationPayload(item.hostId!, item.session!)));
      case TrayAction.toggleMute:
        final n = _ref.read(notifySettingsProvider.notifier);
        unawaited(n.setMuted(!_ref.read(notifySettingsProvider).muted));
      case TrayAction.quit:
        unawaited(_ref.read(windowServiceProvider).quit());
    }
  }

  Future<void> _showWindow() async {
    await _ref.read(windowServiceProvider).show();
    _ref.read(appVisibleProvider.notifier).set(true);
  }

  void _onAgents(AgentsState s) {
    final settings = _ref.read(notifySettingsProvider);
    final visible = _ref.read(appVisibleProvider)
        ? _ref.read(openSessionProvider)
        : null;
    for (final e in _tracker.update(s)) {
      final req = _policy.decide(e, settings, visibleKey: visible);
      if (req == null) continue;
      unawaited(
        _ref
            .read(localNotifierProvider)
            .show(
              id: req.id,
              title: req.title,
              body: req.body,
              payload: req.payload,
            ),
      );
    }
  }

  void _onTap(String payload) {
    final uri = Uri.tryParse(payload);
    if (uri == null || uri.scheme != 'agentd') return;
    unawaited(_showWindow());
    _emit(uri);
  }

  /// Links emitted before the router listens (cold start) wait.
  void _emit(Uri u) => _links.hasListener ? _links.add(u) : _pending.add(u);

  void dispose() {
    unawaited(_tapSub?.cancel());
    unawaited(_links.close());
  }
}
