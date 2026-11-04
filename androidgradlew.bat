@echo off
rem Forwarder shim: the workflow gate invokes "androidgradlew.bat" from the
rem workspace root (its script contains "android\gradlew.bat" where JS drops
rem the backslash in "\g"). This makes that invocation run the real wrapper
rem with identical arguments.
rem The Gradle build root is android/, but the gate's cwd is the workspace
rem root, so cd there before delegating (same as running gradlew.bat in android/).
cd /d "%~dp0android"
call gradlew.bat %*
exit /b %ERRORLEVEL%
