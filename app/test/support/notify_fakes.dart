import 'dart:async';

import 'package:agentd_app/platform/notifications_platform.dart';

class Shown {
  Shown(this.id, this.title, this.body, this.payload);
  final int id;
  final String title;
  final String body;
  final String payload;
}

class FakeNotifier implements LocalNotifier {
  final shown = <Shown>[];
  // ignore: close_sinks
  final tapCtl = StreamController<String>.broadcast();
  bool granted = false;
  int requests = 0;
  String? launch;

  @override
  Stream<String> get taps => tapCtl.stream;
  @override
  Future<void> init() async {}
  @override
  Future<bool> requestPermission() async {
    requests++;
    granted = true;
    return true;
  }

  @override
  Future<bool> permissionGranted() async => granted;
  @override
  Future<void> show({
    required int id,
    required String title,
    required String body,
    required String payload,
  }) async => shown.add(Shown(id, title, body, payload));
  @override
  Future<String?> launchPayload() async => launch;
}

class FakeTray implements TrayService {
  final updates = <({int count, String tooltip, List<TrayEntry> entries})>[];
  void Function(String id)? select;

  @override
  bool get supported => true;
  @override
  Future<void> update({
    required int count,
    required String tooltip,
    required List<TrayEntry> entries,
    required void Function(String id) onSelect,
  }) async {
    updates.add((count: count, tooltip: tooltip, entries: entries));
    select = onSelect;
  }

  @override
  Future<void> dispose() async {}
}

class FakeWindow implements WindowService {
  Future<void> Function()? onClose;
  final calls = <String>[];
  @override
  Future<void> init({required Future<void> Function() onClose}) async =>
      this.onClose = onClose;
  @override
  Future<void> setKeepInTray(bool keep) async => calls.add('keep:$keep');
  @override
  Future<void> show() async => calls.add('show');
  @override
  Future<void> hide() async => calls.add('hide');
  @override
  Future<void> quit() async => calls.add('quit');
}

class FakeOpener implements LinkOpener {
  final opened = <Uri>[];
  bool result = true;
  @override
  Future<bool> open(Uri url) async {
    opened.add(url);
    return result;
  }
}
