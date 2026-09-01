<?php

declare(strict_types=1);

// Framework-free contract test. CI runs this with php-cli; the small stubs let
// us exercise the listener and bootstrap wiring without checking out all of
// Nextcloud or adding a Composer tree to this deliberately tiny app.

namespace OCP\EventDispatcher {
	class Event {}
	interface IEventListener {
		public function handle(Event $event): void;
	}
}

namespace OCP\Collaboration\Resources {
	class LoadAdditionalScriptsEvent extends \OCP\EventDispatcher\Event {}
}

namespace OCP\AppFramework {
	class App {
		public function __construct(public string $appId) {}
	}
}

namespace OCP\AppFramework\Bootstrap {
	interface IBootContext {}
	interface IBootstrap {
		public function register(IRegistrationContext $context): void;
		public function boot(IBootContext $context): void;
	}
	interface IRegistrationContext {
		public function registerEventListener(string $event, string $listener): void;
	}
}

namespace OCP\AppFramework\Services {
	interface IInitialState {
		public function provideInitialState(string $key, mixed $value): void;
	}
}

namespace OCP {
	interface IRequest {
		public function getParam(string $key, mixed $default = null): mixed;
	}
	interface IURLGenerator {
		public function getWebroot(): string;
	}
	final class Util {
		/** @var list<array{string,string}> */
		public static array $scripts = [];
		public static function addScript(string $app, string $script): void {
			self::$scripts[] = [$app, $script];
		}
	}
}

namespace OCA\AppAPI\Service {
	class ExAppConfigService {
		/** @param list<array{configkey:string,configvalue:string}>|null $values */
		public function __construct(
			private ?array $values,
			private bool $fails = false,
		) {
		}
		/** @return list<array{configkey:string,configvalue:string}>|null */
		public function getAppConfigValues(string $appId, array $configKeys): ?array {
			\CassiniCaptureTests\check($appId === 'gocassini', 'listener read the wrong ExApp config');
			\CassiniCaptureTests\check($configKeys === ['source_capture_enabled'], 'listener read the wrong config key');
			if ($this->fails) {
				throw new \RuntimeException('broken AppAPI config');
			}
			return $this->values;
		}
	}
}

namespace CassiniCaptureTests {
	use OCA\AppAPI\Service\ExAppConfigService;
	use OCA\CassiniCapture\AppInfo\Application;
	use OCA\CassiniCapture\Listener\LoadTalkCaptureScriptListener;
	use OCP\AppFramework\Bootstrap\IRegistrationContext;
	use OCP\AppFramework\Services\IInitialState;
	use OCP\Collaboration\Resources\LoadAdditionalScriptsEvent;
	use OCP\IRequest;
	use OCP\IURLGenerator;
	use OCP\Util;

	require_once __DIR__ . '/../lib/Listener/LoadTalkCaptureScriptListener.php';
	require_once __DIR__ . '/../lib/AppInfo/Application.php';

	function check(bool $condition, string $message): void {
		if (!$condition) {
			fwrite(STDERR, "FAIL: $message\n");
			exit(1);
		}
	}

	final class Request implements IRequest {
		public function __construct(private string $route) {}
		public function getParam(string $key, mixed $default = null): mixed {
			return $key === '_route' ? $this->route : $default;
		}
	}

	final class InitialState implements IInitialState {
		/** @var array<string,mixed> */
		public array $values = [];
		public function provideInitialState(string $key, mixed $value): void {
			$this->values[$key] = $value;
		}
	}

	final class URLGenerator implements IURLGenerator {
		public function getWebroot(): string { return '/nextcloud/'; }
	}

	function appConfig(bool $enabled, bool $fails = false): ExAppConfigService {
		return new ExAppConfigService([[
			'configkey' => 'source_capture_enabled',
			'configvalue' => $enabled ? 'true' : 'false',
		]], $fails);
	}

	function listener(string $route, bool $config = true): array {
		Util::$scripts = [];
		$state = new InitialState();
		$listener = new LoadTalkCaptureScriptListener(
			new Request($route),
			$state,
			new URLGenerator(),
			appConfig($config),
		);
		$listener->handle(new LoadAdditionalScriptsEvent());
		$listener->handle(new LoadAdditionalScriptsEvent());
		return [$state->values, Util::$scripts];
	}

	foreach (['spreed.Page.showCall', 'spreed.page.authenticatepassword', 'spreed.Page.index'] as $route) {
		[$state, $scripts] = listener($route);
		check(count($scripts) === 1, "$route did not load exactly once");
		check($scripts[0] === ['cassini_capture', 'capture-payload'], "$route loaded the wrong script");
		check($state['capture'] === [
			'enabled' => true,
			'proxyBase' => '/nextcloud/index.php/apps/app_api/proxy/gocassini',
		], "$route injected wrong initial state");
	}

	foreach (['spreed.Page.recording', 'files.View.index', 'activity.Activities.showList', 'polls.page.index'] as $route) {
		[$state, $scripts] = listener($route);
		check($state === [] && $scripts === [], "$route unexpectedly loaded capture");
	}

	[$state] = listener('spreed.Page.showCall', false);
	check($state['capture']['enabled'] === false, 'disabled switch was not fail-closed');

	Util::$scripts = [];
	$state = new InitialState();
	$missing = new LoadTalkCaptureScriptListener(
		new Request('spreed.Page.showCall'),
		$state,
		new URLGenerator(),
		new ExAppConfigService(null),
	);
	$missing->handle(new LoadAdditionalScriptsEvent());
	check($state->values['capture']['enabled'] === false, 'missing ExApp config was not fail-closed');

	Util::$scripts = [];
	$state = new InitialState();
	$failing = new LoadTalkCaptureScriptListener(
		new Request('spreed.Page.showCall'),
		$state,
		new URLGenerator(),
		appConfig(true, true),
	);
	$failing->handle(new LoadAdditionalScriptsEvent());
	check($state->values === [] && Util::$scripts === [], 'configuration failure could break or instrument Talk');

	final class Registration implements IRegistrationContext {
		/** @var list<array{string,string}> */
		public array $listeners = [];
		public function registerEventListener(string $event, string $listener): void {
			$this->listeners[] = [$event, $listener];
		}
	}
	$registration = new Registration();
	(new Application())->register($registration);
	check($registration->listeners === [[
		LoadAdditionalScriptsEvent::class,
		LoadTalkCaptureScriptListener::class,
	]], 'Application did not register the sanctioned Talk-script event');

	fwrite(STDOUT, "OK: cassini_capture listener contracts\n");
}
