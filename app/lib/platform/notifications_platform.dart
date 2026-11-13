/// Plugin seams for notifications, tray, window and links (AGED-US-0032).
/// Real implementations wrap flutter_local_notifications, tray_manager,
/// window_manager and url_launcher; tests use fakes. Every real call is
/// best effort: a missing notification server or tray never breaks the app.
// The tray_manager / window_manager 0.9 / 0.7 "legacy" API is the stable
// declarative one; the native API is still moving.
// ignore_for_file: deprecated_member_use
library;

import 'dart:async';
import 'dart:convert';
import 'dart:io';
import 'dart:ui' as ui;

import 'package:flutter/foundation.dart';
import 'package:flutter_local_notifications/flutter_local_notifications.dart';
import 'package:tray_manager/legacy.dart';
import 'package:url_launcher/url_launcher.dart';
import 'package:window_manager/legacy.dart';

bool get _underTest => Platform.environment.containsKey('FLUTTER_TEST');
bool get isDesktop =>
    Platform.isLinux || Platform.isWindows || Platform.isMacOS;

/// Shows local notifications and reports taps (payload = deep link).
abstract class LocalNotifier {
  Future<void> init();

  /// Android 13+ runtime permission; true when granted (or not needed).
  Future<bool> requestPermission();

  /// Whether notifications are currently allowed (true off Android 13+).
  Future<bool> permissionGranted();

  Future<void> show({
    required int id,
    required String title,
    required String body,
    required String payload,
  });

  /// Payloads of tapped notifications.
  Stream<String> get taps;

  /// Payload of the notification that launched the app, if any.
  Future<String?> launchPayload();
}

class PluginLocalNotifier implements LocalNotifier {
  final _plugin = FlutterLocalNotificationsPlugin();
  final _taps = StreamController<String>.broadcast();
  bool _ready = false;

  @override
  Stream<String> get taps => _taps.stream;

  @override
  Future<void> init() async {
    if (_underTest || kIsWeb) return;
    try {
      _ready =
          await _plugin.initialize(
            settings: const InitializationSettings(
              android: AndroidInitializationSettings('@mipmap/ic_launcher'),
              linux: LinuxInitializationSettings(defaultActionName: 'Open'),
              macOS: DarwinInitializationSettings(),
              windows: WindowsInitializationSettings(
                appName: 'agentd',
                appUserModelId: 'digio.agentd.app',
                guid: '6f1c3a52-9d2e-4b7a-8f10-5a3e2c9b7d41',
              ),
            ),
            onDidReceiveNotificationResponse: (r) {
              final p = r.payload;
              if (p != null && p.isNotEmpty) _taps.add(p);
            },
          ) ??
          false;
    } on Object catch (e) {
      debugPrint('notifications init failed: $e');
    }
  }

  AndroidFlutterLocalNotificationsPlugin? get _android => _plugin
      .resolvePlatformSpecificImplementation<
        AndroidFlutterLocalNotificationsPlugin
      >();

  @override
  Future<bool> requestPermission() async {
    if (_underTest || !Platform.isAndroid) return true;
    try {
      return await _android?.requestNotificationsPermission() ?? false;
    } on Object {
      return false;
    }
  }

  @override
  Future<bool> permissionGranted() async {
    if (_underTest || !Platform.isAndroid) return true;
    try {
      return await _android?.areNotificationsEnabled() ?? false;
    } on Object {
      return false;
    }
  }

  @override
  Future<void> show({
    required int id,
    required String title,
    required String body,
    required String payload,
  }) async {
    if (!_ready) return;
    try {
      await _plugin.show(
        id: id,
        title: title,
        body: body,
        payload: payload,
        notificationDetails: const NotificationDetails(
          android: AndroidNotificationDetails(
            'attention',
            'Agents needing attention',
            channelDescription: 'A session is waiting, finished or exited.',
            importance: Importance.high,
            priority: Priority.high,
          ),
          linux: LinuxNotificationDetails(),
          macOS: DarwinNotificationDetails(),
        ),
      );
    } on Object catch (e) {
      debugPrint('notification failed: $e');
    }
  }

  @override
  Future<String?> launchPayload() async {
    if (_underTest || !_ready) return null;
    try {
      final d = await _plugin.getNotificationAppLaunchDetails();
      if (d?.didNotificationLaunchApp == true) {
        return d?.notificationResponse?.payload;
      }
    } on Object {
      // not supported on this platform
    }
    return null;
  }
}

/// One tray menu entry for [TrayService.update].
class TrayEntry {
  const TrayEntry(this.id, this.label, {this.separatorBefore = false});
  final String id;
  final String label;
  final bool separatorBefore;
}

/// The desktop tray icon.
abstract class TrayService {
  bool get supported;

  /// Draws the icon with [count], sets the tooltip and the menu. [onSelect]
  /// receives the entry id.
  Future<void> update({
    required int count,
    required String tooltip,
    required List<TrayEntry> entries,
    required void Function(String id) onSelect,
  });

  Future<void> dispose();
}

class PluginTrayService implements TrayService {
  bool _ok = true;
  int? _drawnCount;

  @override
  bool get supported => isDesktop && !_underTest && _ok;

  @override
  Future<void> update({
    required int count,
    required String tooltip,
    required List<TrayEntry> entries,
    required void Function(String id) onSelect,
  }) async {
    if (!supported) return;
    try {
      if (_drawnCount != count) {
        _drawnCount = count;
        await trayManager.setIcon(await _iconDataUri(count));
      }
      await trayManager.setToolTip(tooltip);
      if (Platform.isMacOS) {
        await trayManager.setTitle(count > 0 ? '$count' : '');
      }
      await trayManager.setContextMenu(
        Menu(
          items: [
            for (final e in entries) ...[
              if (e.separatorBefore) MenuItem.separator(),
              MenuItem(
                key: e.id,
                label: e.label,
                onClick: (_) => onSelect(e.id),
              ),
            ],
          ],
        ),
      );
    } on Object catch (e) {
      _ok = false;
      debugPrint('tray unavailable: $e');
    }
  }

  @override
  Future<void> dispose() async {
    if (!supported) return;
    try {
      await trayManager.destroy();
    } on Object {
      // ignore
    }
  }

  /// A teal disc, with a red badge when [count] > 0, as a PNG data URI.
  Future<String> _iconDataUri(int count) async {
    const size = 64.0;
    final rec = ui.PictureRecorder();
    final c = ui.Canvas(rec);
    c.drawCircle(
      const ui.Offset(size / 2, size / 2),
      size / 2 - 4,
      ui.Paint()..color = const ui.Color(0xFF00897B),
    );
    c.drawRect(
      const ui.Rect.fromLTWH(20, 24, 6, 16),
      ui.Paint()..color = const ui.Color(0xFFFFFFFF),
    );
    c.drawRect(
      const ui.Rect.fromLTWH(30, 34, 14, 6),
      ui.Paint()..color = const ui.Color(0xFFFFFFFF),
    );
    if (count > 0) {
      c.drawCircle(
        const ui.Offset(size - 14, 14),
        14,
        ui.Paint()..color = const ui.Color(0xFFD32F2F),
      );
      final pb =
          ui.ParagraphBuilder(
              ui.ParagraphStyle(textAlign: ui.TextAlign.center, fontSize: 18),
            )
            ..pushStyle(
              ui.TextStyle(
                color: const ui.Color(0xFFFFFFFF),
                fontWeight: ui.FontWeight.bold,
              ),
            )
            ..addText(count > 9 ? '9+' : '$count');
      final p = pb.build()..layout(const ui.ParagraphConstraints(width: 28));
      c.drawParagraph(p, const ui.Offset(size - 28, 3));
    }
    final img = await rec.endRecording().toImage(size.toInt(), size.toInt());
    final bytes = await img.toByteData(format: ui.ImageByteFormat.png);
    return 'data:image/png;base64,${base64Encode(bytes!.buffer.asUint8List())}';
  }
}

/// Main-window control (hide on close, show from the tray).
abstract class WindowService {
  /// Prevent closing: [onClose] is called instead, to hide or quit.
  Future<void> init({required Future<void> Function() onClose});
  Future<void> setKeepInTray(bool keep);
  Future<void> show();
  Future<void> hide();
  Future<void> quit();
}

class PluginWindowService with WindowListener implements WindowService {
  Future<void> Function()? _onClose;
  bool _ok = false;

  @override
  Future<void> init({required Future<void> Function() onClose}) async {
    if (_underTest || !isDesktop) return;
    _onClose = onClose;
    try {
      await windowManager.ensureInitialized();
      windowManager.addListener(this);
      _ok = true;
    } on Object catch (e) {
      debugPrint('window manager unavailable: $e');
    }
  }

  @override
  Future<void> setKeepInTray(bool keep) async {
    if (!_ok) return;
    try {
      await windowManager.setPreventClose(keep);
    } on Object {
      // ignore
    }
  }

  @override
  Future<void> show() async {
    if (!_ok) return;
    try {
      await windowManager.show();
      await windowManager.focus();
    } on Object {
      // ignore
    }
  }

  @override
  Future<void> hide() async {
    if (!_ok) return;
    try {
      await windowManager.hide();
    } on Object {
      // ignore
    }
  }

  @override
  Future<void> quit() async {
    try {
      if (_ok) await windowManager.setPreventClose(false);
      if (_ok) await windowManager.destroy();
    } on Object {
      // ignore
    }
    exit(0);
  }

  @override
  void onWindowClose() {
    final cb = _onClose;
    if (cb != null) unawaited(cb());
  }
}

/// Opens external links / apps (ntfy).
abstract class LinkOpener {
  /// Opens [url]; false when nothing handles it.
  Future<bool> open(Uri url);
}

class UrlLauncherOpener implements LinkOpener {
  @override
  Future<bool> open(Uri url) async {
    if (_underTest) return false;
    try {
      return await launchUrl(url, mode: LaunchMode.externalApplication);
    } on Object {
      return false;
    }
  }
}
