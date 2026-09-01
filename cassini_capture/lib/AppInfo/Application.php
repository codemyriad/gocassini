<?php

declare(strict_types=1);

namespace OCA\CassiniCapture\AppInfo;

use OCA\CassiniCapture\Listener\LoadTalkCaptureScriptListener;
use OCP\AppFramework\App;
use OCP\AppFramework\Bootstrap\IBootContext;
use OCP\AppFramework\Bootstrap\IBootstrap;
use OCP\AppFramework\Bootstrap\IRegistrationContext;
use OCP\Collaboration\Resources\LoadAdditionalScriptsEvent;

final class Application extends App implements IBootstrap {
	public const APP_ID = 'cassini_capture';

	public function __construct() {
		parent::__construct(self::APP_ID);
	}

	public function register(IRegistrationContext $context): void {
		$context->registerEventListener(
			LoadAdditionalScriptsEvent::class,
			LoadTalkCaptureScriptListener::class,
		);
	}

	public function boot(IBootContext $context): void {
	}
}
