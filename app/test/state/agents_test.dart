import 'dart:async';

import 'package:agentd_app/api/agentd_api.dart';
import 'package:agentd_app/state/agents.dart';
import 'package:agentd_app/state/providers.dart';
import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:flutter_test/flutter_test.dart';

import '../support/agents_fakes.dart';
import '../support/fakes.dart';

void main() {
  late FakeFeeds feeds;
  late ProviderContainer c;
  late StreamController<void> trigger;

  Future<void> boot([List<dynamic>? hosts]) async {
    c = ProviderContainer(
      // ignore: argument_type_not_assignable
      overrides: agentsOverrides(
        feeds,
        (hosts ?? [sampleHost()]).cast(),
        trigger: trigger.stream,
      ).cast(),
    );
    c.listen(agentsProvider, (_, _) {});
    await settle();
  }

  AgentsState st() => c.read(agentsProvider);
  List<String> names() => [for (final e in st().entries) e.name];
  SessionSummary sOf(String n) =>
      st().entries.firstWhere((e) => e.name == n).session;

  setUp(() {
    feeds = FakeFeeds();
    trigger = StreamController<void>.broadcast();
  });
  tearDown(() {
    c.dispose();
    unawaited(trigger.close());
  });

  test('initial list is loaded and sorted', () async {
    feeds.lists['h1'] = [
      sess('a@x', status: Status.idle),
      sess('b@x', status: Status.waiting),
      sess('c@x', status: Status.working, attention: true),
    ];
    await boot();
    expect(st().loading, isFalse);
    expect(names(), ['c@x', 'b@x', 'a@x']);
    expect(st().hostOnline('h1'), isTrue);
    expect(st().entries.first.attention, isTrue);
  });

  test('events update the list', () async {
    feeds.lists['h1'] = [sess('a@x'), sess('b@x')];
    await boot();
    final f = feeds.last('h1');
    f.emit(
      'status.changed',
      session: 'a@x',
      data: {'from': 'idle', 'to': 'waiting', 'attention': true, 'revision': 3},
    );
    await settle();
    expect(sOf('a@x').status, Status.waiting);
    expect(st().entries.first.name, 'a@x');
    expect(st().entries.first.attention, isTrue);

    f.emit('attention.changed', session: 'a@x', data: {'attention': false});
    f.emit('input.changed', session: 'a@x', data: {'state': 'draft'});
    await settle();
    expect(sOf('a@x').attention, isFalse);
    expect(sOf('a@x').inputState, InputState.draft);

    f.emit(
      'session.created',
      session: 'n@x',
      data: {'name': 'n@x', 'state': 'running', 'status': 'idle', 'cwd': '/p'},
    );
    await settle();
    expect(names(), contains('n@x'));

    f.emit('session.exited', session: 'b@x', data: {'exit_code': 2});
    await settle();
    expect(sOf('b@x').state, SessionState.exited);
    expect(sOf('b@x').exitCode, 2);
    expect(st().entries.last.name, 'b@x');

    f.emit('session.removed', session: 'b@x');
    await settle();
    expect(names(), isNot(contains('b@x')));
  });

  test('duplicate created events do not duplicate rows', () async {
    feeds.lists['h1'] = [sess('a@x')];
    await boot();
    final f = feeds.last('h1');
    for (var i = 0; i < 3; i++) {
      f.emit('session.created', session: 'a@x', data: {'name': 'a@x'});
    }
    await settle();
    expect(names(), ['a@x']);
  });

  test(
    'events arriving during the initial list are applied after it',
    () async {
      final gate = Completer<void>();
      feeds.lists['h1'] = [sess('a@x')];
      c = ProviderContainer(
        overrides: [
          ...agentsOverrides(
            feeds,
            [sampleHost()],
            factory: (ref, host) async {
              final f = _SlowFeed(feeds, host.id, gate.future);
              feeds.feeds.putIfAbsent(host.id, () => []).add(f);
              return f;
            },
          ),
        ].cast(),
      );
      c.listen(agentsProvider, (_, _) {});
      await settle();
      feeds
          .last('h1')
          .emit('status.changed', session: 'a@x', data: {'to': 'working'});
      await settle();
      expect(st().loading, isTrue);
      gate.complete();
      await settle();
      expect(sOf('a@x').status, Status.working);
    },
  );

  test('resync refetches and replaces the list', () async {
    feeds.lists['h1'] = [sess('a@x'), sess('b@x')];
    await boot();
    feeds.lists['h1'] = [sess('z@x')];
    feeds.last('h1').emit('resync', data: {'reason': 'overflow'});
    await settle();
    expect(names(), ['z@x']);
  });

  test('hello with a new epoch refetches, same epoch does not', () async {
    feeds.lists['h1'] = [sess('a@x')];
    await boot();
    final f = feeds.last('h1');
    f.emit('hello', data: {'epoch': 'E1'});
    await settle();
    final calls = f.listCalls;
    f.emit('hello', data: {'epoch': 'E1'});
    await settle();
    expect(f.listCalls, calls);
    feeds.lists['h1'] = [sess('b@x')];
    f.emit('hello', data: {'epoch': 'E2'});
    await settle();
    expect(f.listCalls, calls + 1);
    expect(names(), ['b@x']);
  });

  test('view.changed refreshes previews with a throttled refetch', () async {
    feeds.lists['h1'] = [sess('a@x', preview: 'old')];
    await boot();
    feeds.lists['h1'] = [sess('a@x', preview: 'new')];
    final f = feeds.last('h1');
    f.emit('view.changed', session: 'a@x', data: {'revision': 9});
    f.emit('view.changed', session: 'a@x', data: {'revision': 10});
    await Future<void>.delayed(const Duration(milliseconds: 60));
    expect(sOf('a@x').preview, 'new');
    expect(f.listCalls, 2);
  });

  test('host offline keeps sessions and marks them', () async {
    feeds.lists['h1'] = [sess('a@x')];
    await boot();
    feeds.last('h1').states$.add(SseState.reconnecting);
    await settle();
    expect(st().hostOnline('h1'), isFalse);
    expect(st().entries.single.hostOnline, isFalse);
    expect(names(), ['a@x']);
    feeds.last('h1').states$.add(SseState.connected);
    await settle();
    expect(st().entries.single.hostOnline, isTrue);
  });

  test('first list failure: offline, then retried', () async {
    feeds.listError['h1'] = ConnectionException('down');
    await boot();
    expect(st().hostOnline('h1'), isFalse);
    expect(st().hosts['h1']!.error, 'down');
    expect(st().loading, isFalse);
    feeds.listError.clear();
    feeds.lists['h1'] = [sess('a@x')];
    await Future<void>.delayed(const Duration(milliseconds: 80));
    expect(names(), ['a@x']);
    expect(st().hostOnline('h1'), isTrue);
  });

  test('auth error is reported and not retried', () async {
    feeds.listError['h1'] = ApiException(
      statusCode: 401,
      code: 'unauthorized',
      message: 'bad token',
    );
    await boot();
    expect(st().hosts['h1']!.authError, isTrue);
    await Future<void>.delayed(const Duration(milliseconds: 80));
    expect(feeds.feeds['h1']!.length, 1);
  });

  test('two hosts are merged, one offline', () async {
    feeds.lists['h1'] = [sess('a@x')];
    feeds.lists['h2'] = [sess('b@y')];
    await boot([sampleHost(), sampleHost(id: 'h2', label: 'laptop')]);
    expect(names().toSet(), {'a@x', 'b@y'});
    feeds.last('h2').states$.add(SseState.reconnecting);
    await settle();
    expect(st().entries.firstWhere((e) => e.name == 'b@y').hostOnline, isFalse);
    expect(st().entries.firstWhere((e) => e.name == 'a@x').hostOnline, isTrue);
    expect(st().entries.firstWhere((e) => e.name == 'b@y').hostLabel, 'laptop');
  });

  test('resume trigger reconnects without duplicating rows', () async {
    feeds.lists['h1'] = [sess('a@x')];
    await boot();
    trigger.add(null);
    await settle();
    expect(feeds.feeds['h1']!.length, 2);
    expect(feeds.feeds['h1']!.first.closed, isTrue);
    expect(names(), ['a@x']);
    // The old stream is dead: its events are ignored.
    feeds.feeds['h1']!.first.emit('session.removed', session: 'a@x');
    await settle();
    expect(names(), ['a@x']);
  });

  test('refresh refetches every host', () async {
    feeds.lists['h1'] = [sess('a@x')];
    await boot();
    feeds.lists['h1'] = [sess('a@x'), sess('b@x')];
    await c.read(agentsProvider.notifier).refresh();
    await settle();
    expect(names().toSet(), {'a@x', 'b@x'});
  });

  test('markSeen clears attention until it is raised again', () async {
    feeds.lists['h1'] = [sess('a@x', attention: true)];
    await boot();
    c.read(agentsProvider.notifier).markSeen('h1', 'a@x');
    expect(st().entries.single.attention, isFalse);
    feeds
        .last('h1')
        .emit('attention.changed', session: 'a@x', data: {'attention': true});
    await settle();
    expect(st().entries.single.attention, isTrue);
  });

  test('removed host stops its feed', () async {
    feeds.lists['h1'] = [sess('a@x')];
    await boot();
    await c.read(hostsProvider.notifier).remove('h1');
    await settle();
    expect(feeds.last('h1').closed, isTrue);
    expect(st().entries, isEmpty);
  });

  test('project is the part after @, else the cwd basename', () {
    expect(projectOf(sess('codex@webapp')), 'webapp');
    expect(projectOf(sess('plain', cwd: '/srv/api/')), 'api');
    expect(projectOf(sess('plain', cwd: '')), '');
  });
}

class _SlowFeed extends FakeFeed {
  _SlowFeed(super.owner, super.hostId, this.gate);
  final Future<void> gate;
  @override
  Future<SessionList> list() async {
    await gate;
    return super.list();
  }
}
