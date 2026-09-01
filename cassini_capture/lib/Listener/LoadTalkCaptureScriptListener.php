<?php

declare(strict_types=1);

namespace OCA\CassiniCapture\Listener;

use OCA\CassiniCapture\AppInfo\Application;
use OCP\App\IAppManager;
use OCP\AppFramework\Services\IInitialState;
use OCP\Collaboration\Resources\LoadAdditionalScriptsEvent;
use OCP\EventDispatcher\Event;
use OCP\EventDispatcher\IEventListener;
use OCP\IAppConfig;
use OCP\IRequest;
use OCP\IURLGenerator;
use OCP\Util;

/** @template-implements IEventListener<LoadAdditionalScriptsEvent> */
final class LoadTalkCaptureScriptListener implements IEventListener {
	private const EXAPP_ID = 'gocassini';
	private const ENABLED_CONFIG_KEY = 'source_capture_enabled';

	/** @var list<string> */
	private const TALK_CALL_ROUTES = [
		'spreed.page.showcall',
		'spreed.page.authenticatepassword',
		'spreed.page.index',
	];

	private bool $loaded = false;

	public function __construct(
		private readonly IRequest $request,
		private readonly IInitialState $initialState,
		private readonly IURLGenerator $urlGenerator,
		private readonly IAppConfig $appConfig,
		private readonly IAppManager $appManager,
	) {
	}

	public static function isTalkCallRoute(string $route): bool {
		return in_array(strtolower($route), self::TALK_CALL_ROUTES, true);
	}

	public function handle(Event $event): void {
		if (!$event instanceof LoadAdditionalScriptsEvent || $this->loaded) {
			return;
		}
		try {
			$route = (string)$this->request->getParam('_route', '');
			if (!self::isTalkCallRoute($route)) {
				return;
			}

			// AppAPI's ExAppConfigService stores every value through setValueString,
			// even when the JSON input was boolean. Reading it with getValueBool would
			// therefore raise an AppConfig type-conflict exception on NC 32+.
			$configured = strtolower($this->appConfig->getValueString(
				self::EXAPP_ID,
				self::ENABLED_CONFIG_KEY,
				'false',
				lazy: true,
			));
			$enabled = $this->appManager->isEnabledForUser(self::EXAPP_ID)
				&& in_array($configured, ['1', 'true', 'yes', 'on'], true);
			$webroot = rtrim($this->urlGenerator->getWebroot(), '/');
			$this->initialState->provideInitialState('capture', [
				'enabled' => $enabled,
				'proxyBase' => $webroot . '/index.php/apps/app_api/proxy/' . self::EXAPP_ID,
			]);
			Util::addScript(Application::APP_ID, 'capture-payload');
			$this->loaded = true;
		} catch (\Throwable) {
			// A capture integration failure must never turn into a Talk outage. The
			// absence of both script and initial state is the fail-closed outcome.
			return;
		}
	}
}
