package com.privstack.panel.di

import dagger.Module
import dagger.hilt.InstallIn
import dagger.hilt.components.SingletonComponent

/**
 * Hilt module for application-scoped dependencies.
 *
 * Most core singletons are already annotated with @Inject constructor() + @Singleton
 * and are auto-discovered by Hilt:
 *
 * - [com.privstack.panel.ipc.PrivctlExecutor]
 * - [com.privstack.panel.ipc.DaemonClient]
 * - [com.privstack.panel.ipc.PollingStatusSource]
 * - [com.privstack.panel.repository.ProfileRepository]
 * - [com.privstack.panel.repository.StatusRepository]
 * - [com.privstack.panel.advisor.AppClassifier]
 *
 * Add @Provides or @Binds methods here for dependencies that require
 * manual construction (e.g. third-party classes, interfaces, or
 * context-dependent configuration).
 */
@Module
@InstallIn(SingletonComponent::class)
object AppModule
