package com.rknnovpn.panel.di

import dagger.Module
import dagger.hilt.InstallIn
import dagger.hilt.components.SingletonComponent

/**
 * Hilt module for application-scoped dependencies.
 *
 * Most core singletons are already annotated with @Inject constructor() + @Singleton
 * and are auto-discovered by Hilt:
 *
 * - [com.rknnovpn.panel.ipc.PrivctlExecutor]
 * - [com.rknnovpn.panel.ipc.DaemonClient]
 * - [com.rknnovpn.panel.ipc.PollingStatusSource]
 * - [com.rknnovpn.panel.repository.ProfileRepository]
 * - [com.rknnovpn.panel.repository.StatusRepository]
 * - [com.rknnovpn.panel.advisor.AppClassifier]
 *
 * Add @Provides or @Binds methods here for dependencies that require
 * manual construction (e.g. third-party classes, interfaces, or
 * context-dependent configuration).
 */
@Module
@InstallIn(SingletonComponent::class)
object AppModule
