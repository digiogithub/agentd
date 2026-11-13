import 'dart:io';

import 'package:agentd_app/api/agentd_api.dart';
import 'package:agentd_app/state/agents.dart';
import 'package:agentd_app/state/notifications.dart';
import 'package:agentd_app/state/providers.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';

import '../support/agents_fakes.dart';
import '../support/fakes.dart';
import '../support/notify_fakes.dart';

void main() {
  late FakeFeeds feeds;
  late FakeNotifier notifier;
  late FakeTray tray;
  late FakeWindow window;
  late Directory dir;
  late ProviderContainer c;

  Future<void> boot({bool desktop = true}) async {
    c = ProviderContainer(
      overrides: [
        // ignore: argument_type_not_assignable
        ...agentsOverrides(feeds, [
          sampleHost(),
          sampleHost(id: 'h2', label: 'two'),
        ]).cast(),
        appSupportDirProvider.overrideWith((ref) async => dir),
        localNotifierProvider.overrideWithValue(notifier),
        trayServiceProvider.overrideWithValue(tray),
        windowServiceProvider.overrideWithValue(window),
        isDesktopProvider.overrideWithValue(desktop),
      ],
    );
    c.listen(agentsProvider, (_, _) {});
    c.listen(notificationControllerProvider, (_, _) {});
    await settle();
  }

  setUp(() {
    feeds = FakeFeeds();
    notifier = FakeNotifier();
    tray = FakeTray();
    window = FakeWindow();
    dir = Directory.systemTemp.createTempSync('agentd_notify_test');
  });
  tearDown(() {
    c.dispose();
    dir.deleteSync(recursive: true);
  });

  test(
    'attention raises one notification; the initial list does not',
    () async {
      feeds.lists['h1'] = [sess('a@x', attention: true), sess('b@x')];
      await boot();
      expect(notifier.shown, isEmpty);
      feeds
          .last('h1')
          .emit(
            'attention.changed',
            session: 'b@x',
            data: {'attention': true, 'reason': 'turn_done'},
          );
      await settle();
      expect(notifier.shown, hasLength(1));
      expect(notifier.shown.single.title, 'b@x on box');
      expect(notifier.shown.single.body, 'b@x finished its turn');
      expect(notifier.shown.single.payload, 'agentd://open?h=h1&s=b%40x');
    },
  );

  test('exit notifies; global mute and per-host mute suppress', () async {
    feeds.lists['h1'] = [sess('a@x', status: Status.working)];
    feeds.lists['h2'] = [sess('z@x', status: Status.working)];
    await boot();
    await c.read(notifySettingsProvider.notifier).setHostMuted('h2', true);
    feeds
        .last('h2')
        .emit('session.exited', session: 'z@x', data: {'exit_code': 0});
    await settle();
    expect(notifier.shown, isEmpty);
    feeds
        .last('h1')
        .emit('session.exited', session: 'a@x', data: {'exit_code': 0});
    await settle();
    expect(notifier.shown.single.body, 'a@x exited');
  });

  test('the open and visible session is not notified', () async {
    feeds.lists['h1'] = [sess('a@x'), sess('b@x')];
    await boot();
    c.read(openSessionProvider.notifier).set('h1/a@x');
    feeds
        .last('h1')
        .emit('attention.changed', session: 'a@x', data: {'attention': true});
    await settle();
    expect(notifier.shown, isEmpty);
    c.read(appVisibleProvider.notifier).set(false);
    feeds
        .last('h1')
        .emit('attention.changed', session: 'b@x', data: {'attention': true});
    feeds
        .last('h1')
        .emit('attention.changed', session: 'a@x', data: {'attention': false});
    feeds
        .last('h1')
        .emit('attention.changed', session: 'a@x', data: {'attention': true});
    await settle();
    expect(notifier.shown.map((s) => s.title), ['b@x on box', 'a@x on box']);
  });

  test(
    'tap emits the deep link; a launch payload is delivered after listen',
    () async {
      notifier.launch = 'agentd://open?h=h1&s=a%40x';
      await boot();
      final got = <Uri>[];
      final sub = c.read(notificationTapsProvider).listen(got.add);
      await settle();
      expect(got.single.toString(), 'agentd://open?h=h1&s=a%40x');
      notifier.tapCtl.add('agentd://open?h=h2&s=z');
      notifier.tapCtl.add('https://evil.example/x');
      await settle();
      expect(got.map((u) => u.queryParameters['h']), ['h1', 'h2']);
      expect(window.calls, contains('show'));
      await sub.cancel();
    },
  );

  test('tray follows attention and its entries act', () async {
    feeds.lists['h1'] = [sess('a@x', attention: true), sess('b@x')];
    await boot();
    expect(tray.updates.last.count, 1);
    final ids = {for (final e in tray.updates.last.entries) e.label: e.id};
    expect(
      ids.keys,
      containsAll(['Open agentd', 'a@x on box', 'Mute notifications', 'Quit']),
    );
    final got = <Uri>[];
    final sub = c.read(notificationTapsProvider).listen(got.add);
    tray.select!(ids['a@x on box']!);
    await settle();
    expect(got.single.queryParameters['s'], 'a@x');
    tray.select!(ids['Mute notifications']!);
    await settle();
    expect(c.read(notifySettingsProvider).muted, isTrue);
    expect(
      tray.updates.last.entries.map((e) => e.label),
      contains('Unmute notifications'),
    );
    tray.select!(ids['Quit']!);
    await settle();
    expect(window.calls, contains('quit'));
    await sub.cancel();
  });

  test('closing the window hides when keepInTray, quits otherwise', () async {
    await boot();
    await window.onClose!();
    expect(window.calls, contains('hide'));
    await c.read(notifySettingsProvider.notifier).setKeepInTray(false);
    window.calls.clear();
    await window.onClose!();
    expect(window.calls, contains('quit'));
    expect(c.read(notifySettingsProvider).keepInTray, isFalse);
  });

  test('settings persist across containers; defaults are safe', () async {
    await boot();
    final d = c.read(notifySettingsProvider);
    expect(d.muted || d.includePreview, isFalse);
    final n = c.read(notifySettingsProvider.notifier);
    await n.setMuted(true);
    await n.setIncludePreview(true);
    await n.setHostMuted('h1', true);
    final store = NotifySettingsStore(dir);
    final s = await store.load();
    expect(s.muted && s.includePreview, isTrue);
    expect(s.mutedHosts, {'h1'});
    final c2 = ProviderContainer(
      overrides: [appSupportDirProvider.overrideWith((ref) async => dir)],
    );
    addTearDown(c2.dispose);
    c2.listen(notifySettingsProvider, (_, _) {});
    for (var i = 0; i < 100 && !c2.read(notifySettingsProvider).muted; i++) {
      await Future<void>.delayed(const Duration(milliseconds: 20));
    }
    expect(c2.read(notifySettingsProvider).muted, isTrue);
  });

  test('corrupt settings file falls back to defaults', () async {
    File('${dir.path}/notifications.json').writeAsStringSync('{nope');
    final s = await NotifySettingsStore(dir).load();
    expect(s.muted, isFalse);
  });
}
